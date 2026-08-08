package hitl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// haxEnvKeys are every environment variable the hax substrate reads. Tests
// clear all of them up front so a stray value in the runner's environment
// cannot leak into a case that expects the variable absent.
var haxEnvKeys = []string{
	"HAX_API_KEY", "HAX_SDK_URL",
	"HAX_SENDER_NAME", "HAX_SENDER_KEY", "NODE_ID",
	"AGENTFIELD_PUBLIC_URL", "AGENTFIELD_SERVER",
}

// clearEnv unsets keys for the duration of the test, restoring prior values on
// cleanup. Used to establish a known-absent baseline before selectively setting
// variables with setEnv (t.Setenv can set but not unset).
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		k := k
		prev, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

// setEnv sets one variable for the test, restoring the prior state on cleanup.
func setEnv(t *testing.T, key, val string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ptr is a tiny helper for the optional *string CreateRequestParams field.
func ptr(s string) *string { return &s }

// TestCreateRequestWireBody drives a full CreateRequest against an httptest hax
// server and asserts the on-the-wire contract: POST /api/v1/requests, Bearer
// auth, exact camelCase top-level key set, publicKey absent, sender shape.
func TestCreateRequestWireBody(t *testing.T) {
	clearEnv(t, haxEnvKeys...)
	// Deterministic sender: display name explicit, key falls back to it.
	setEnv(t, "HAX_SENDER_NAME", "pr-af")

	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotCT     string
		gotBody   map[string]json.RawMessage
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"req_123","url":"https://hax.example/r/req_123"}`))
	}))
	defer srv.Close()

	c := &HaxClient{BaseURL: srv.URL, APIKey: "test-key-123", HTTPClient: srv.Client()}
	created, err := c.CreateRequest(context.Background(), CreateRequestParams{
		Type:             "pr-af-review-v1",
		Payload:          map[string]any{"title": "T"},
		Title:            "PR-AF Review Approval",
		Description:      ptr("desc"),
		WebhookURL:       "https://cp.example/api/v1/webhooks/approval-response",
		ExpiresInSeconds: 3600,
		UserID:           "u_1",
		Metadata:         map[string]any{"reviewId": "rev_abc"},
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	if created.ID != "req_123" || created.URL != "https://hax.example/r/req_123" {
		t.Fatalf("unexpected CreatedRequest: %+v", created)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/requests" {
		t.Errorf("path = %q, want /api/v1/requests", gotPath)
	}
	if gotAuth != "Bearer test-key-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key-123")
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}

	// Exact camelCase top-level key set — no snake_case leakage, publicKey absent.
	wantKeys := []string{
		"description", "expiresInSeconds", "metadata", "payload",
		"sender", "title", "type", "userId", "webhookUrl",
	}
	if got := sortedKeys(gotBody); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("body keys = %v, want %v", got, wantKeys)
	}
	if _, present := gotBody["publicKey"]; present {
		t.Error("publicKey MUST be omitted from the hax body")
	}
	for _, snake := range []string{"public_key", "webhook_url", "expires_in_seconds", "user_id", "display_name"} {
		if _, present := gotBody[snake]; present {
			t.Errorf("snake_case key %q leaked into hax body", snake)
		}
	}

	var sender map[string]any
	if err := json.Unmarshal(gotBody["sender"], &sender); err != nil {
		t.Fatalf("decode sender: %v", err)
	}
	wantSender := map[string]any{"key": "pr-af", "displayName": "pr-af"}
	if !reflect.DeepEqual(sender, wantSender) {
		t.Errorf("sender = %v, want %v", sender, wantSender)
	}
}

// TestCreateRequestMinimalBody confirms every optional field is omitted when
// unset — only type, payload and the always-present sender remain.
func TestCreateRequestMinimalBody(t *testing.T) {
	clearEnv(t, haxEnvKeys...)
	setEnv(t, "NODE_ID", "pr-af") // sender falls back to NODE_ID

	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"req_1","url":"u"}`))
	}))
	defer srv.Close()

	c := &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()}
	if _, err := c.CreateRequest(context.Background(), CreateRequestParams{
		Type:    "pr-af-review-v1",
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	wantKeys := []string{"payload", "sender", "type"}
	if got := sortedKeys(gotBody); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("minimal body keys = %v, want %v", got, wantKeys)
	}
}

// TestCreateRequestTimeout exercises the watchdog: a slow handler against a
// short client timeout must surface a "wedged" error rather than hang. Uses
// sub-second durations (the 120s constant is overridden via HaxClient.Timeout).
func TestCreateRequestTimeout(t *testing.T) {
	clearEnv(t, haxEnvKeys...)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block until the test releases it, well past the client timeout
	}))
	defer srv.Close()
	defer close(release)

	c := &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client(), Timeout: 20 * time.Millisecond}
	_, err := c.CreateRequest(context.Background(), CreateRequestParams{
		Type:    "pr-af-review-v1",
		Payload: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "wedged") {
		t.Errorf("timeout error = %q, want it to mention 'timed out' and 'wedged'", err.Error())
	}
}

// TestCreateRequestErrorPaths covers a non-2xx status and a 200 response that
// omits the id — both must be reported as errors, not swallowed.
func TestCreateRequestErrorPaths(t *testing.T) {
	clearEnv(t, haxEnvKeys...)

	t.Run("non-2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"bad severity enum"}`))
		}))
		defer srv.Close()
		c := &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()}
		_, err := c.CreateRequest(context.Background(), CreateRequestParams{Type: "t", Payload: map[string]any{}})
		if err == nil || !strings.Contains(err.Error(), "status 422") {
			t.Fatalf("err = %v, want it to mention status 422", err)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"url":"u"}`))
		}))
		defer srv.Close()
		c := &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()}
		_, err := c.CreateRequest(context.Background(), CreateRequestParams{Type: "t", Payload: map[string]any{}})
		if err == nil || !strings.Contains(err.Error(), "missing id") {
			t.Fatalf("err = %v, want it to mention missing id", err)
		}
	})
}

// TestBuildHaxClientFromEnv is the HITL on/off switch: nil without a key,
// configured client with one. Table-driven over HAX_API_KEY / HAX_SDK_URL.
func TestBuildHaxClientFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      *string // nil => leave unset
		sdkURL      *string // nil => leave unset
		wantNil     bool
		wantBaseURL string
	}{
		{name: "no key -> nil (HITL disabled)", apiKey: nil, wantNil: true},
		{name: "blank key -> nil", apiKey: ptr("   "), wantNil: true},
		{name: "empty key -> nil", apiKey: ptr(""), wantNil: true},
		{name: "key, default base", apiKey: ptr("secret"), sdkURL: nil, wantBaseURL: "http://localhost:3000"},
		{name: "key + custom base, trailing slash trimmed", apiKey: ptr("secret"), sdkURL: ptr("https://hax.internal:9000/"), wantBaseURL: "https://hax.internal:9000"},
		{name: "key, key is trimmed", apiKey: ptr("  secret  "), sdkURL: ptr("http://h:1"), wantBaseURL: "http://h:1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, haxEnvKeys...)
			if tc.apiKey != nil {
				setEnv(t, "HAX_API_KEY", *tc.apiKey)
			}
			if tc.sdkURL != nil {
				setEnv(t, "HAX_SDK_URL", *tc.sdkURL)
			}

			c := BuildHaxClientFromEnv()
			if tc.wantNil {
				if c != nil {
					t.Fatalf("expected nil client (HITL disabled), got %+v", c)
				}
				return
			}
			if c == nil {
				t.Fatal("expected a client, got nil")
			}
			if c.BaseURL != tc.wantBaseURL {
				t.Errorf("BaseURL = %q, want %q", c.BaseURL, tc.wantBaseURL)
			}
			if strings.TrimSpace(c.APIKey) == "" {
				t.Error("APIKey must be populated and trimmed")
			}
		})
	}
}

// TestResolveSender covers the sender-attribution cascade
// (HAX_SENDER_NAME || NODE_ID || "pr-af") and the key fallback
// (HAX_SENDER_KEY, defaulting to the resolved display name).
func TestResolveSender(t *testing.T) {
	tests := []struct {
		name               string
		senderName, nodeID *string
		senderKey          *string
		wantKey, wantName  string
	}{
		{name: "all unset -> pr-af/pr-af", wantKey: "pr-af", wantName: "pr-af"},
		{name: "NODE_ID drives both (design case)", nodeID: ptr("pr-af"), wantKey: "pr-af", wantName: "pr-af"},
		{name: "HAX_SENDER_NAME wins over NODE_ID", senderName: ptr("custom"), nodeID: ptr("pr-af"), wantKey: "custom", wantName: "custom"},
		{name: "explicit key overrides name-derived key", senderName: ptr("disp"), senderKey: ptr("k-explicit"), wantKey: "k-explicit", wantName: "disp"},
		{name: "empty HAX_SENDER_NAME falls through to NODE_ID", senderName: ptr(""), nodeID: ptr("pr-af"), wantKey: "pr-af", wantName: "pr-af"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, haxEnvKeys...)
			if tc.senderName != nil {
				setEnv(t, "HAX_SENDER_NAME", *tc.senderName)
			}
			if tc.nodeID != nil {
				setEnv(t, "NODE_ID", *tc.nodeID)
			}
			if tc.senderKey != nil {
				setEnv(t, "HAX_SENDER_KEY", *tc.senderKey)
			}
			key, name := resolveSender()
			if key != tc.wantKey || name != tc.wantName {
				t.Errorf("resolveSender() = (%q, %q), want (%q, %q)", key, name, tc.wantKey, tc.wantName)
			}
		})
	}
}

// TestApprovalWebhookURL covers the three-tier precedence
// (AGENTFIELD_PUBLIC_URL > agentFieldServer arg > AGENTFIELD_SERVER) plus
// trailing-slash trimming and the nil result when nothing resolves.
func TestApprovalWebhookURL(t *testing.T) {
	const suffix = "/api/v1/webhooks/approval-response"
	tests := []struct {
		name      string
		publicURL *string
		serverArg string
		serverEnv *string
		want      *string
	}{
		{name: "public url wins over arg and env", publicURL: ptr("https://public.example"), serverArg: "http://internal:8080", serverEnv: ptr("http://also:8080"), want: ptr("https://public.example" + suffix)},
		{name: "arg used when public url unset", serverArg: "http://internal:8080", serverEnv: ptr("http://also:8080"), want: ptr("http://internal:8080" + suffix)},
		{name: "server env used when public url + arg empty", serverEnv: ptr("http://cp:8080"), want: ptr("http://cp:8080" + suffix)},
		{name: "trailing slash trimmed", publicURL: ptr("https://public.example/"), want: ptr("https://public.example" + suffix)},
		{name: "nothing resolves -> nil", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, "AGENTFIELD_PUBLIC_URL", "AGENTFIELD_SERVER")
			if tc.publicURL != nil {
				setEnv(t, "AGENTFIELD_PUBLIC_URL", *tc.publicURL)
			}
			if tc.serverEnv != nil {
				setEnv(t, "AGENTFIELD_SERVER", *tc.serverEnv)
			}
			got := ApprovalWebhookURL(tc.serverArg)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("got nil, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("got %q, want %q", *got, *tc.want)
			}
		})
	}
}
