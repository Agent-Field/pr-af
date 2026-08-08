package node

// webhook.go ports the GitHub webhook (app.py:250-367): issue_comment mentions
// and pull_request labels fire an async PR review at the control plane.
//
// Env reads happen at REQUEST time (matching internal/config's call-time
// convention) so the httptest table can drive GITHUB_WEBHOOK_SECRET /
// PR_AF_BOT_MENTION with t.Setenv. Python reads these at import; the observable
// behavior is identical for a fixed environment.

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBotMention   = "@pr-af"
	defaultLabelTrigger = "pr-af"
	deliveryCacheSize   = 1024
	labelFireTTL        = 10 * time.Minute
)

type webhookDedupe struct {
	mu              sync.Mutex
	deliveries      *list.List
	deliveryEntries map[string]*list.Element
	recentPRs       map[string]time.Time
	now             func() time.Time
}

// claim guards label-triggered dispatches. The state is intentionally local to
// one node process and resets on restart; multi-process deployments need a
// shared store to dedupe across workers.
func (d *webhookDedupe) claim(deliveryID, prURL string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deliveries == nil {
		d.deliveries = list.New()
		d.deliveryEntries = map[string]*list.Element{}
		d.recentPRs = map[string]time.Time{}
	}
	if deliveryID != "" {
		if elem := d.deliveryEntries[deliveryID]; elem != nil {
			d.deliveries.MoveToBack(elem)
			return "duplicate delivery"
		}
		delivery := d.deliveries.PushBack(deliveryID)
		d.deliveryEntries[deliveryID] = delivery
		if d.deliveries.Len() > deliveryCacheSize {
			oldest := d.deliveries.Front()
			delete(d.deliveryEntries, oldest.Value.(string))
			d.deliveries.Remove(oldest)
		}
	}
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	for url, firedAt := range d.recentPRs {
		if now.Sub(firedAt) >= labelFireTTL {
			delete(d.recentPRs, url)
		}
	}
	if firedAt, ok := d.recentPRs[prURL]; ok && now.Sub(firedAt) < labelFireTTL {
		return "recently dispatched"
	}
	d.recentPRs[prURL] = now
	return ""
}

// webhookGitHub handles POST /webhook/github. It mirrors app.py::webhook_github:
// verify the HMAC signature, answer ping with pong, dispatch matching label and
// mention triggers, and ignore all other events.
func (n *Node) webhookGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "cannot read body"})
		return
	}
	defer r.Body.Close()

	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	sig := r.Header.Get("X-Hub-Signature-256")
	if secret != "" && !verifySignature(body, sig, secret) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Invalid signature"})
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pong"})
		return
	}
	if event != "issue_comment" && event != "pull_request" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "event=" + event})
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON"})
		return
	}
	if event == "pull_request" {
		n.handlePullRequestWebhook(w, r, payload)
		return
	}

	action := getStr(payload, "action")
	if action != "created" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "action=" + action})
		return
	}

	botMention := envOr("PR_AF_BOT_MENTION", defaultBotMention)
	commentBody := nestedStr(payload, "comment", "body")
	if !strings.Contains(strings.ToLower(commentBody), strings.ToLower(botMention)) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "no bot mention"})
		return
	}

	// Only respond to comments on PRs (issue_comment fires for plain issues too).
	prURL := getPRURLFromIssue(payload)
	if prURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "not a PR comment"})
		return
	}

	hints := extractHintsFromComment(commentBody, botMention)
	execID := n.fireReview(r.Context(), prURL, hints)

	// Python returns execution_id=None (JSON null) when the fire fails and the id
	// string on success; execID is nil / string here to preserve that.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "review_dispatched",
		"pr_url":       prURL,
		"execution_id": execID,
	})
}

func (n *Node) handlePullRequestWebhook(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	action := getStr(payload, "action")
	if action != "labeled" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "pr action=" + action})
		return
	}
	if nestedStr(payload, "label", "name") != envOr("PR_AF_LABEL", defaultLabelTrigger) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "label not a trigger"})
		return
	}
	prURL := nestedStr(payload, "pull_request", "html_url")
	if prURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "no pr_url"})
		return
	}
	if reason := n.labelDedupe.claim(r.Header.Get("X-GitHub-Delivery"), prURL); reason != "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": reason})
		return
	}
	execID := n.fireReview(r.Context(), prURL, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "review_dispatched",
		"pr_url":       prURL,
		"execution_id": execID,
	})
}

// verifySignature ports app.py::_verify_signature. An empty secret means "no
// secret configured — skip verification" (the caller already guards that).
func verifySignature(payload []byte, signature, secret string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// fireReview ports app.py::_fire_review: POST {CP}/api/v1/execute/async/
// {node_id}.review with body {"input": {pr_url, depth:"standard", dry_run:false
// [, hints]}}. Returns the execution id string on success, or nil (JSON null) on
// any failure — a fire-and-forget dispatch, so failures never block the webhook.
func (n *Node) fireReview(ctx context.Context, prURL string, hints []string) any {
	inputPayload := map[string]any{
		"pr_url":  prURL,
		"depth":   "standard",
		"dry_run": false,
	}
	if len(hints) > 0 {
		inputPayload["hints"] = hints
	}
	for key, value := range webhookReviewLimits() {
		inputPayload[key] = value
	}
	body, err := json.Marshal(map[string]any{"input": inputPayload})
	if err != nil {
		return nil
	}

	target := strings.TrimSuffix(n.AgentFieldServer, "/") + "/api/v1/execute/async/" + n.NodeID + ".review"
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("AGENTFIELD_API_KEY"); apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := n.webhookClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 { // Python: resp.raise_for_status()
		return nil
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	if id, ok := parsed["execution_id"].(string); ok {
		return id
	}
	return nil
}

// webhookReviewLimits reads optional per-deployment caps for each dispatch.
// Invalid values are deployment mistakes, but must never turn a webhook into a
// 500 or create an unusable zero-cap review, so they are logged and omitted.
func webhookReviewLimits() map[string]any {
	limits := map[string]any{}
	for _, cap := range []struct {
		env string
		key string
		min int
	}{
		{"PR_AF_MAX_CONCURRENT_REVIEWERS", "max_concurrent_reviewers", 1},
		{"PR_AF_MAX_REVIEW_DEPTH", "max_review_depth", 0},
		{"PR_AF_MAX_COVERAGE_ITERATIONS", "max_coverage_iterations", 1},
	} {
		raw := os.Getenv(cap.env)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < cap.min {
			log.Printf("[PR-AF] Ignoring invalid %s=%q (must be an integer >= %d)", cap.env, raw, cap.min)
			continue
		}
		limits[cap.key] = value
	}
	return limits
}

// extractHintsFromComment ports app.py::_extract_hints_from_comment: the text
// after the (case-insensitive) bot mention, trimmed; [] when there is none.
func extractHintsFromComment(commentBody, botMention string) []string {
	mention := strings.ToLower(botMention)
	lower := strings.ToLower(commentBody)
	idx := strings.Index(lower, mention)
	if idx < 0 {
		return nil
	}
	after := strings.TrimSpace(commentBody[idx+len(botMention):])
	if after != "" {
		return []string{after}
	}
	return nil
}

// getPRURLFromIssue ports app.py::_get_pr_url_from_issue: payload.issue.
// pull_request.html_url, or "" when absent (issue_comment on a non-PR issue).
func getPRURLFromIssue(payload map[string]any) string {
	return nestedStr(payload, "issue", "pull_request", "html_url")
}

// getStr returns payload[key] as a string, or "" when absent / non-string.
func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// nestedStr walks a chain of object keys and returns the terminal string value,
// or "" if any hop is missing or not an object/string (Python's chained
// dict.get({}).get(...) with a string leaf).
func nestedStr(m map[string]any, keys ...string) string {
	cur := m
	for i, k := range keys {
		v, ok := cur[k]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			if s, ok := v.(string); ok {
				return s
			}
			return ""
		}
		next, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

// writeJSON writes a JSON response with the given status (the SDK's writeJSON is
// unexported, so the webhook has its own).
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}
