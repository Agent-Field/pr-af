package node

// webhook.go ports the GitHub @mention webhook (app.py:250-367): an
// issue_comment listener that fires an async PR review at the control plane when
// someone comments "@pr-af …" on a PR.
//
// Env reads happen at REQUEST time (matching internal/config's call-time
// convention) so the httptest table can drive GITHUB_WEBHOOK_SECRET /
// PR_AF_BOT_MENTION with t.Setenv. Python reads these at import; the observable
// behavior is identical for a fixed environment.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultBotMention = "@pr-af"

// webhookGitHub handles POST /webhook/github. It mirrors app.py::webhook_github:
// verify the HMAC signature, answer ping with pong, ignore anything that is not
// a created issue_comment carrying the bot mention on a PR, then fire the async
// review and echo the execution id.
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
	if event != "issue_comment" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "event=" + event})
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON"})
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

	client := &http.Client{Timeout: 15 * time.Second}
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
