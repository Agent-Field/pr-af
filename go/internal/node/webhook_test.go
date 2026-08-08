package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeCP records the async-execute request the webhook fires and returns a
// canned execution id, standing in for the control plane.
type fakeCP struct {
	url       string
	client    *http.Client
	gotPath   string
	gotBody   map[string]any
	gotAPIKey string
	hitCount  int
}

func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	cp := &fakeCP{url: "http://control-plane.test"}
	cp.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		cp.hitCount++
		cp.gotPath = r.URL.Path
		cp.gotAPIKey = r.Header.Get("X-API-Key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &cp.gotBody)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"execution_id":"exec_abc123"}`)),
			Request:    r,
		}, nil
	})}
	return cp
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// prComment builds an issue_comment webhook payload for a PR with the given
// comment body. htmlURL "" produces a non-PR issue (no pull_request block).
func prComment(action, commentBody, htmlURL string) []byte {
	issue := map[string]any{"number": 42}
	if htmlURL != "" {
		issue["pull_request"] = map[string]any{"html_url": htmlURL}
	}
	payload := map[string]any{
		"action":     action,
		"comment":    map[string]any{"body": commentBody},
		"issue":      issue,
		"repository": map[string]any{"full_name": "octo/repo"},
	}
	b, _ := json.Marshal(payload)
	return b
}

func prLabeled(action, label, htmlURL string) []byte {
	payload := map[string]any{
		"action":       action,
		"label":        map[string]any{"name": label},
		"pull_request": map[string]any{"number": 42, "html_url": htmlURL},
		"repository":   map[string]any{"full_name": "octo/repo"},
	}
	b, _ := json.Marshal(payload)
	return b
}

// doWebhook drives n.webhookGitHub with the given event/signature and returns
// the recorded response plus the decoded JSON body.
func doWebhook(t *testing.T, n *Node, event, signature string, body []byte) (*httptest.ResponseRecorder, map[string]any) {
	return doWebhookDelivery(t, n, event, signature, "", body)
}

func doWebhookDelivery(t *testing.T, n *Node, event, signature, delivery string, body []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(string(body)))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	rec := httptest.NewRecorder()
	n.webhookGitHub(rec, req)

	var decoded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec, decoded
}

func TestWebhookPing(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	n := &Node{NodeID: "pr-af"}
	rec, body := doWebhook(t, n, "ping", "", []byte(`{}`))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["status"] != "pong" {
		t.Errorf("body = %v, want status=pong", body)
	}
}

func TestWebhookSignature(t *testing.T) {
	const secret = "s3cr3t"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	n := &Node{NodeID: "pr-af"}
	body := prComment("created", "@pr-af review", "https://github.com/octo/repo/pull/42")

	t.Run("valid signature passes verification (ping through)", func(t *testing.T) {
		// A valid signature on a ping still returns pong — verification succeeded.
		rec, resp := doWebhook(t, n, "ping", sign(secret, []byte(`{}`)), []byte(`{}`))
		if rec.Code != 200 || resp["status"] != "pong" {
			t.Errorf("valid signature rejected: code=%d body=%v", rec.Code, resp)
		}
	})

	t.Run("invalid signature -> 401", func(t *testing.T) {
		rec, _ := doWebhook(t, n, "issue_comment", "sha256=deadbeef", body)
		if rec.Code != 401 {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("missing signature -> 401", func(t *testing.T) {
		rec, _ := doWebhook(t, n, "issue_comment", "", body)
		if rec.Code != 401 {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

func TestWebhookIgnoreGates(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	n := &Node{NodeID: "pr-af"}
	prURL := "https://github.com/octo/repo/pull/42"

	cases := []struct {
		name       string
		event      string
		body       []byte
		wantReason string
	}{
		{"non-issue_comment event", "push", []byte(`{}`), "event=push"},
		{"action not created", "issue_comment", prComment("edited", "@pr-af", prURL), "action=edited"},
		{"no bot mention", "issue_comment", prComment("created", "looks good to me", prURL), "no bot mention"},
		{"comment on a non-PR issue", "issue_comment", prComment("created", "@pr-af", ""), "not a PR comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := doWebhook(t, n, tc.event, "", tc.body)
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if resp["status"] != "ignored" {
				t.Errorf("status = %v, want ignored", resp["status"])
			}
			if resp["reason"] != tc.wantReason {
				t.Errorf("reason = %q, want %q", resp["reason"], tc.wantReason)
			}
		})
	}
}

func TestWebhookFiresAsyncReview(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}
	prURL := "https://github.com/octo/repo/pull/42"

	rec, resp := doWebhook(t, n, "issue_comment", "",
		prComment("created", "@pr-af please focus on error handling", prURL))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if resp["status"] != "review_dispatched" {
		t.Errorf("status = %v, want review_dispatched", resp["status"])
	}
	if resp["pr_url"] != prURL {
		t.Errorf("pr_url = %v, want %q", resp["pr_url"], prURL)
	}
	if resp["execution_id"] != "exec_abc123" {
		t.Errorf("execution_id = %v, want exec_abc123", resp["execution_id"])
	}

	// The CP received exactly one POST to the node-qualified async endpoint.
	if cp.hitCount != 1 {
		t.Fatalf("CP hit %d times, want 1", cp.hitCount)
	}
	if cp.gotPath != "/api/v1/execute/async/pr-af.review" {
		t.Errorf("fire path = %q, want /api/v1/execute/async/pr-af.review", cp.gotPath)
	}
	input, ok := cp.gotBody["input"].(map[string]any)
	if !ok {
		t.Fatalf("fire body missing input: %v", cp.gotBody)
	}
	if input["pr_url"] != prURL {
		t.Errorf("fired pr_url = %v, want %q", input["pr_url"], prURL)
	}
	if input["depth"] != "standard" {
		t.Errorf("fired depth = %v, want standard", input["depth"])
	}
	if input["dry_run"] != false {
		t.Errorf("fired dry_run = %v, want false", input["dry_run"])
	}
	hints, ok := input["hints"].([]any)
	if !ok || len(hints) != 1 || hints[0] != "please focus on error handling" {
		t.Errorf("fired hints = %v, want [please focus on error handling]", input["hints"])
	}
}

func TestWebhookForwardsControlPlaneAPIKey(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}
	prURL := "https://github.com/octo/repo/pull/42"

	t.Setenv("AGENTFIELD_API_KEY", "cp-secret")
	doWebhook(t, n, "issue_comment", "", prComment("created", "@pr-af", prURL))
	if cp.gotAPIKey != "cp-secret" {
		t.Errorf("X-API-Key = %q, want cp-secret", cp.gotAPIKey)
	}

	t.Setenv("AGENTFIELD_API_KEY", "")
	doWebhook(t, n, "issue_comment", "", prComment("created", "@pr-af", prURL))
	if cp.gotAPIKey != "" {
		t.Errorf("X-API-Key = %q, want absent", cp.gotAPIKey)
	}
}

func TestWebhookLabelTrigger(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	cp := newFakeCP(t)
	n := &Node{NodeID: "custom-node", AgentFieldServer: cp.url, webhookClient: cp.client}
	prURL := "https://github.com/octo/repo/pull/42"

	_, resp := doWebhook(t, n, "pull_request", "", prLabeled("opened", "pr-af", prURL))
	if resp["status"] != "ignored" || resp["reason"] != "pr action=opened" {
		t.Fatalf("non-labeled action response = %v", resp)
	}
	_, resp = doWebhook(t, n, "pull_request", "", prLabeled("labeled", "other", prURL))
	if resp["status"] != "ignored" || resp["reason"] != "label not a trigger" {
		t.Fatalf("wrong-label response = %v", resp)
	}
	_, resp = doWebhook(t, n, "pull_request", "", prLabeled("labeled", "pr-af", prURL))
	if resp["status"] != "review_dispatched" || cp.hitCount != 1 {
		t.Fatalf("default-label response = %v, CP hits = %d", resp, cp.hitCount)
	}
	if cp.gotPath != "/api/v1/execute/async/custom-node.review" {
		t.Errorf("fire path = %q, want custom node endpoint", cp.gotPath)
	}

	t.Setenv("PR_AF_LABEL", "ready-for-ai")
	overrideURL := "https://github.com/octo/repo/pull/43"
	_, resp = doWebhook(t, n, "pull_request", "", prLabeled("labeled", "pr-af", overrideURL))
	if resp["status"] != "ignored" {
		t.Fatalf("default label under override response = %v", resp)
	}
	_, resp = doWebhook(t, n, "pull_request", "", prLabeled("labeled", "ready-for-ai", overrideURL))
	if resp["status"] != "review_dispatched" || cp.hitCount != 2 {
		t.Fatalf("override-label response = %v, CP hits = %d", resp, cp.hitCount)
	}
}

func TestWebhookReviewCaps(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("PR_AF_MAX_CONCURRENT_REVIEWERS", "2")
	t.Setenv("PR_AF_MAX_REVIEW_DEPTH", "0")
	t.Setenv("PR_AF_MAX_COVERAGE_ITERATIONS", "3")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}

	doWebhook(t, n, "issue_comment", "", prComment("created", "@pr-af", "https://github.com/octo/repo/pull/42"))
	input := cp.gotBody["input"].(map[string]any)
	for key, want := range map[string]float64{
		"max_concurrent_reviewers": 2,
		"max_review_depth":         0,
		"max_coverage_iterations":  3,
	} {
		if input[key] != want {
			t.Errorf("%s = %v, want %v", key, input[key], want)
		}
	}
}

func TestWebhookInvalidReviewCapsAreIgnored(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("PR_AF_MAX_CONCURRENT_REVIEWERS", "0")
	t.Setenv("PR_AF_MAX_REVIEW_DEPTH", "-1")
	t.Setenv("PR_AF_MAX_COVERAGE_ITERATIONS", "many")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}

	rec, resp := doWebhook(t, n, "issue_comment", "", prComment("created", "@pr-af", "https://github.com/octo/repo/pull/42"))
	if rec.Code != http.StatusOK || resp["status"] != "review_dispatched" {
		t.Fatalf("invalid caps response: code=%d body=%v", rec.Code, resp)
	}
	input := cp.gotBody["input"].(map[string]any)
	for _, key := range []string{"max_concurrent_reviewers", "max_review_depth", "max_coverage_iterations"} {
		if _, ok := input[key]; ok {
			t.Errorf("invalid cap %s unexpectedly forwarded: %v", key, input[key])
		}
	}
}

func TestWebhookLabelDedupe(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}
	now := time.Unix(1000, 0)
	n.labelDedupe.now = func() time.Time { return now }
	payload := prLabeled("labeled", "pr-af", "https://github.com/octo/repo/pull/42")

	_, first := doWebhookDelivery(t, n, "pull_request", "", "delivery-1", payload)
	_, duplicate := doWebhookDelivery(t, n, "pull_request", "", "delivery-1", payload)
	_, recent := doWebhookDelivery(t, n, "pull_request", "", "delivery-2", payload)
	now = now.Add(labelFireTTL + time.Second)
	_, afterTTL := doWebhookDelivery(t, n, "pull_request", "", "delivery-3", payload)

	if first["status"] != "review_dispatched" || afterTTL["status"] != "review_dispatched" {
		t.Errorf("first/after-TTL responses = %v / %v", first, afterTTL)
	}
	if duplicate["reason"] != "duplicate delivery" {
		t.Errorf("duplicate response = %v", duplicate)
	}
	if recent["reason"] != "recently dispatched" {
		t.Errorf("recent response = %v", recent)
	}
	if cp.hitCount != 2 {
		t.Errorf("CP hit %d times, want 2", cp.hitCount)
	}
}

func TestWebhookBotMentionOverride(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("PR_AF_BOT_MENTION", "@reviewbot")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.url, webhookClient: cp.client}
	prURL := "https://github.com/octo/repo/pull/7"

	// The default "@pr-af" no longer triggers; the configured "@reviewbot" does.
	rec, resp := doWebhook(t, n, "issue_comment", "",
		prComment("created", "@pr-af nope", prURL))
	if resp["status"] != "ignored" || resp["reason"] != "no bot mention" {
		t.Errorf("@pr-af should be ignored under override: code=%d body=%v", rec.Code, resp)
	}

	rec, resp = doWebhook(t, n, "issue_comment", "",
		prComment("created", "@reviewbot go", prURL))
	if resp["status"] != "review_dispatched" {
		t.Errorf("@reviewbot should dispatch: code=%d body=%v", rec.Code, resp)
	}
	if cp.hitCount != 1 {
		t.Errorf("CP hit %d times, want 1 (only @reviewbot fires)", cp.hitCount)
	}
}
