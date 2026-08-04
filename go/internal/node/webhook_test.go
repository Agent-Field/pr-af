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
)

// fakeCP records the async-execute request the webhook fires and returns a
// canned execution id, standing in for the control plane.
type fakeCP struct {
	server   *httptest.Server
	gotPath  string
	gotBody  map[string]any
	hitCount int
}

func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	cp := &fakeCP{}
	cp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.hitCount++
		cp.gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &cp.gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"execution_id":"exec_abc123"}`))
	}))
	t.Cleanup(cp.server.Close)
	return cp
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

// doWebhook drives n.webhookGitHub with the given event/signature and returns
// the recorded response plus the decoded JSON body.
func doWebhook(t *testing.T, n *Node, event, signature string, body []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(string(body)))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
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
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.server.URL}
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

func TestWebhookBotMentionOverride(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("PR_AF_BOT_MENTION", "@reviewbot")
	cp := newFakeCP(t)
	n := &Node{NodeID: "pr-af", AgentFieldServer: cp.server.URL}
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
