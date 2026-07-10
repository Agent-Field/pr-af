package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
	"github.com/golang-jwt/jwt/v5"
)

// ---- helpers ---------------------------------------------------------------

func testClient(baseURL, token string) *client {
	return &client{
		token:      token,
		baseURL:    baseURL,
		httpClient: &http.Client{},
		tokenCache: map[int]cachedToken{},
		sleepFn:    func(context.Context, time.Duration) error { return nil },
	}
}

func testAppClient(baseURL, appID, pem string) *client {
	return &client{
		appID:         appID,
		appPrivateKey: pem,
		useAppAuth:    true,
		baseURL:       baseURL,
		httpClient:    &http.Client{},
		tokenCache:    map[int]cachedToken{},
		sleepFn:       func(context.Context, time.Duration) error { return nil },
	}
}

func genRSAKeyPEM(t *testing.T) (privPEM string, pub *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return string(block), &key.PublicKey
}

// ---- ParsePRURL ------------------------------------------------------------

func TestParsePRURL(t *testing.T) {
	c := testClient("http://x", "")
	cases := []struct {
		url     string
		owner   string
		repo    string
		number  int
		wantErr bool
	}{
		{"https://github.com/octocat/hello/pull/123", "octocat", "hello", 123, false},
		{"http://github.com/foo/bar/pull/7", "foo", "bar", 7, false},
		{"https://github.com/o/r/pull/123/files", "o", "r", 123, false},         // trailing path allowed
		{"https://github.com/o/r/pull/123#discussion_r1", "o", "r", 123, false}, // fragment allowed
		{"https://github.com/o/r/pull/12abc", "o", "r", 12, false},              // not end-anchored: digits only
		{"https://gitlab.com/o/r/pull/1", "", "", 0, true},
		{"https://github.com/o/r/pulls/1", "", "", 0, true},
		{"https://github.com/o/r/pull/abc", "", "", 0, true},
		{"not a url", "", "", 0, true},
		{"", "", "", 0, true},
	}
	for _, tc := range cases {
		owner, repo, number, err := c.ParsePRURL(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePRURL(%q): expected error, got none", tc.url)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePRURL(%q): unexpected error: %v", tc.url, err)
			continue
		}
		if owner != tc.owner || repo != tc.repo || number != tc.number {
			t.Errorf("ParsePRURL(%q) = (%q,%q,%d), want (%q,%q,%d)",
				tc.url, owner, repo, number, tc.owner, tc.repo, tc.number)
		}
	}
}

// ---- fake GitHub for FetchPR ----------------------------------------------

const metaJSON = `{"title":"Add feature","body":"Body text","labels":[{"name":"bug"},{"name":""},{"name":"enhancement"}],"user":{"login":"octocat"},"base":{"sha":"basesha111"},"head":{"sha":"headsha222"}}`

const diffText = "diff --git a/x b/x\n@@ -0,0 +1 @@\n+hi\n"

type ghRec struct {
	mu          sync.Mutex
	metaCalls   int
	filesPages  map[int]bool
	perPageFile string
	filesAccept string
	auth        string
	metaAccept  string
	diffAccept  string
	sawDiff     bool
	metaStatus  func(call int) int
}

func filesPage(page int) []map[string]any {
	switch page {
	case 1:
		out := make([]map[string]any, 100)
		for i := range out {
			out[i] = map[string]any{
				"filename":  fmt.Sprintf("f%d.go", i),
				"status":    "modified",
				"additions": i,
				"deletions": 0,
				"patch":     "@@",
			}
		}
		return out
	case 2:
		return []map[string]any{
			{"filename": "normal.go", "status": "added", "additions": 1, "deletions": 0, "patch": "@@"},
			{"filename": "renamed_new.go", "status": "renamed", "previous_filename": "renamed_old.go", "additions": 0, "deletions": 0, "patch": ""},
			{"filename": "nostatus.go", "additions": 2, "deletions": 1, "patch": "@@"}, // no "status" key -> default "modified"
		}
	default:
		return []map[string]any{}
	}
}

func commitsPage(page int) []map[string]any {
	switch page {
	case 1:
		out := make([]map[string]any, 100)
		for i := range out {
			msg := fmt.Sprintf("c%d", i)
			if i == 0 {
				msg = "" // empty message -> dropped
			}
			out[i] = map[string]any{"commit": map[string]any{"message": msg}}
		}
		return out
	case 2:
		out := make([]map[string]any, 100)
		for i := range out {
			out[i] = map[string]any{"commit": map[string]any{"message": fmt.Sprintf("d%d", i)}}
		}
		return out
	default:
		return []map[string]any{} // empty page -> break
	}
}

func newFetchServer(t *testing.T, rec *ghRec) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerFetchHandlers(mux, rec)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// registerFetchHandlers wires the PR metadata/files/commits/diff endpoints onto
// mux so they can be combined with the App installation-token endpoints.
func registerFetchHandlers(mux *http.ServeMux, rec *ghRec) {
	if rec.filesPages == nil {
		rec.filesPages = map[int]bool{}
	}

	// metadata + diff share this path (branch on Accept header).
	mux.HandleFunc("/repos/o/r/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.auth = r.Header.Get("Authorization")
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			rec.sawDiff = true
			rec.diffAccept = r.Header.Get("Accept")
			rec.mu.Unlock()
			_, _ = io.WriteString(w, diffText)
			return
		}
		rec.metaCalls++
		call := rec.metaCalls
		rec.metaAccept = r.Header.Get("Accept")
		statusFn := rec.metaStatus
		rec.mu.Unlock()

		status := http.StatusOK
		if statusFn != nil {
			status = statusFn(call)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, metaJSON)
	})

	mux.HandleFunc("/repos/o/r/pulls/7/files", func(w http.ResponseWriter, r *http.Request) {
		page := atoiDefault(r.URL.Query().Get("page"), 1)
		rec.mu.Lock()
		rec.filesPages[page] = true
		rec.perPageFile = r.URL.Query().Get("per_page")
		rec.filesAccept = r.Header.Get("Accept")
		rec.mu.Unlock()
		_ = json.NewEncoder(w).Encode(filesPage(page))
	})

	mux.HandleFunc("/repos/o/r/pulls/7/commits", func(w http.ResponseWriter, r *http.Request) {
		page := atoiDefault(r.URL.Query().Get("page"), 1)
		_ = json.NewEncoder(w).Encode(commitsPage(page))
	})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestFetchPR_Success(t *testing.T) {
	rec := &ghRec{}
	srv := newFetchServer(t, rec)
	c := testClient(srv.URL, "test-token")

	data, err := c.FetchPR(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("FetchPR: %v", err)
	}

	if data.Owner != "o" || data.Repo != "r" || data.Number != 7 {
		t.Errorf("owner/repo/number = %q/%q/%d", data.Owner, data.Repo, data.Number)
	}
	if data.Title != "Add feature" {
		t.Errorf("title = %q", data.Title)
	}
	if data.Description != "Body text" {
		t.Errorf("description = %q", data.Description)
	}
	if want := []string{"bug", "enhancement"}; !equalStrings(data.Labels, want) {
		t.Errorf("labels = %v, want %v (empty-name label must be dropped)", data.Labels, want)
	}
	if data.Author != "octocat" {
		t.Errorf("author = %q", data.Author)
	}
	if data.BaseSHA != "basesha111" || data.HeadSHA != "headsha222" {
		t.Errorf("base/head sha = %q/%q", data.BaseSHA, data.HeadSHA)
	}
	if len(data.ChangedFiles) != 103 {
		t.Errorf("changed files = %d, want 103 (pagination)", len(data.ChangedFiles))
	}
	if len(data.CommitMessages) != 199 {
		t.Errorf("commit messages = %d, want 199 (empty dropped + pagination)", len(data.CommitMessages))
	}
	if data.Diff != diffText {
		t.Errorf("diff = %q, want %q", data.Diff, diffText)
	}

	// renamed + missing-status defaults
	byPath := map[string]schemas.ChangedFile{}
	for _, f := range data.ChangedFiles {
		byPath[f.Path] = f
	}
	if f, ok := byPath["renamed_new.go"]; !ok {
		t.Error("missing renamed_new.go")
	} else if f.PreviousPath == nil || *f.PreviousPath != "renamed_old.go" || f.Status != "renamed" {
		t.Errorf("renamed file = %+v", f)
	}
	if f, ok := byPath["nostatus.go"]; !ok {
		t.Error("missing nostatus.go")
	} else if f.Status != "modified" {
		t.Errorf("missing-status file Status = %q, want default \"modified\"", f.Status)
	} else if f.PreviousPath != nil {
		t.Errorf("nostatus.go PreviousPath = %v, want nil", *f.PreviousPath)
	}

	// headers + pagination assertions
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.auth != "Bearer test-token" {
		t.Errorf("auth header = %q, want %q", rec.auth, "Bearer test-token")
	}
	if rec.metaAccept != "application/vnd.github+json" {
		t.Errorf("metadata Accept = %q", rec.metaAccept)
	}
	if rec.filesAccept != "application/vnd.github+json" {
		t.Errorf("files Accept = %q", rec.filesAccept)
	}
	if rec.perPageFile != "100" {
		t.Errorf("files per_page = %q, want 100", rec.perPageFile)
	}
	if !rec.sawDiff || rec.diffAccept != "application/vnd.github.v3.diff" {
		t.Errorf("diff request: sawDiff=%v accept=%q", rec.sawDiff, rec.diffAccept)
	}
	if !rec.filesPages[1] || !rec.filesPages[2] {
		t.Errorf("files pagination did not reach page 2: %v", rec.filesPages)
	}
}

func TestFetchPR_RetryThenSucceed(t *testing.T) {
	for _, status := range []int{500, 429, 403} {
		status := status
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			rec := &ghRec{metaStatus: func(call int) int {
				if call == 1 {
					return status
				}
				return 200
			}}
			srv := newFetchServer(t, rec)
			c := testClient(srv.URL, "test-token")

			data, err := c.FetchPR(context.Background(), "o", "r", 7)
			if err != nil {
				t.Fatalf("FetchPR after retry: %v", err)
			}
			if data.Title != "Add feature" {
				t.Errorf("title = %q", data.Title)
			}
			rec.mu.Lock()
			calls := rec.metaCalls
			rec.mu.Unlock()
			if calls != 2 {
				t.Errorf("metadata calls = %d, want 2 (one failure + one success)", calls)
			}
		})
	}
}

func TestFetchPR_FailFastOn404(t *testing.T) {
	rec := &ghRec{metaStatus: func(call int) int { return 404 }}
	srv := newFetchServer(t, rec)
	c := testClient(srv.URL, "test-token")

	_, err := c.FetchPR(context.Background(), "o", "r", 7)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
	rec.mu.Lock()
	calls := rec.metaCalls
	rec.mu.Unlock()
	if calls != 1 {
		t.Errorf("metadata calls = %d, want 1 (fail fast, no retry)", calls)
	}
}

func TestFetchPR_RetriesExhausted(t *testing.T) {
	rec := &ghRec{metaStatus: func(call int) int { return 500 }} // always fails
	srv := newFetchServer(t, rec)
	c := testClient(srv.URL, "test-token")

	_, err := c.FetchPR(context.Background(), "o", "r", 7)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Errorf("expected *APIError 500, got %v", err)
	}
	rec.mu.Lock()
	calls := rec.metaCalls
	rec.mu.Unlock()
	if calls != 4 {
		t.Errorf("metadata calls = %d, want 4 (all attempts)", calls)
	}
}

// ---- PostReview ------------------------------------------------------------

type postRec struct {
	mu       sync.Mutex
	auth     string
	body     []byte
	status   int
	respBody string
}

func newPostServer(t *testing.T, rec *postRec) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.auth = r.Header.Get("Authorization")
		rec.body = b
		status := rec.status
		respBody := rec.respBody
		rec.mu.Unlock()
		if status >= 400 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, respBody)
			return
		}
		_, _ = io.WriteString(w, `{"id":42,"state":"COMMENTED"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPostReview_PayloadShape(t *testing.T) {
	rec := &postRec{}
	srv := newPostServer(t, rec)
	c := testClient(srv.URL, "test-token")

	review := schemas.GitHubReview{
		Body:  "Summary body",
		Event: "COMMENT",
		Comments: []schemas.GitHubComment{
			{Path: "a.go", Line: 5, Side: "RIGHT", Body: "keep me"},
			{Path: "", Line: 9, Side: "RIGHT", Body: "no path -> drop"},
			{Path: "b.go", Line: 0, Side: "LEFT", Body: "line 0 -> drop"},
		},
	}
	result, err := c.PostReview(context.Background(), "o", "r", 7, review, "abc123def456")
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if got, _ := result["id"].(float64); got != 42 {
		t.Errorf("result id = %v, want 42", result["id"])
	}

	rec.mu.Lock()
	body := rec.body
	auth := rec.auth
	rec.mu.Unlock()

	if auth != "Bearer test-token" {
		t.Errorf("auth = %q", auth)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("payload not json: %v (%s)", err, body)
	}
	if payload["body"] != "Summary body" {
		t.Errorf("body = %v", payload["body"])
	}
	if payload["event"] != "COMMENT" {
		t.Errorf("event = %v", payload["event"])
	}
	if payload["commit_id"] != "abc123def456" {
		t.Errorf("commit_id = %v", payload["commit_id"])
	}
	comments, ok := payload["comments"].([]any)
	if !ok {
		t.Fatalf("comments not an array: %v", payload["comments"])
	}
	if len(comments) != 1 {
		t.Fatalf("comments len = %d, want 1 (filtered)", len(comments))
	}
	cm := comments[0].(map[string]any)
	if cm["path"] != "a.go" || cm["line"].(float64) != 5 || cm["side"] != "RIGHT" || cm["body"] != "keep me" {
		t.Errorf("kept comment = %v", cm)
	}
}

func TestPostReview_NoCommitID_NoComments(t *testing.T) {
	rec := &postRec{}
	srv := newPostServer(t, rec)
	c := testClient(srv.URL, "test-token")

	// zero comments -> no "comments" key at all; empty commitID -> no commit_id key
	review := schemas.GitHubReview{Body: "b", Event: "APPROVE", Comments: []schemas.GitHubComment{}}
	if _, err := c.PostReview(context.Background(), "o", "r", 7, review, ""); err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	rec.mu.Lock()
	body := rec.body
	rec.mu.Unlock()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["commit_id"]; ok {
		t.Errorf("commit_id present, want absent: %v", payload)
	}
	if _, ok := payload["comments"]; ok {
		t.Errorf("comments present for empty list, want absent: %v", payload)
	}
}

func TestPostReview_AllCommentsFilteredStillEmitsEmptyList(t *testing.T) {
	rec := &postRec{}
	srv := newPostServer(t, rec)
	c := testClient(srv.URL, "test-token")

	// non-empty original list, but all fail the path/line filter -> "comments": []
	review := schemas.GitHubReview{
		Body:  "b",
		Event: "COMMENT",
		Comments: []schemas.GitHubComment{
			{Path: "", Line: 1, Side: "RIGHT", Body: "x"},
			{Path: "y.go", Line: 0, Side: "RIGHT", Body: "y"},
		},
	}
	if _, err := c.PostReview(context.Background(), "o", "r", 7, review, ""); err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	rec.mu.Lock()
	body := rec.body
	rec.mu.Unlock()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	comments, ok := payload["comments"].([]any)
	if !ok {
		t.Fatalf("comments key absent, want present empty list: %v", payload)
	}
	if len(comments) != 0 {
		t.Errorf("comments len = %d, want 0", len(comments))
	}
}

func TestPostReview_422TypedError(t *testing.T) {
	rec := &postRec{
		status:   422,
		respBody: `{"message":"Unprocessable Entity","errors":[{"resource":"PullRequestReview","code":"custom","message":"Review cannot be submitted on your own pull request"}]}`,
	}
	srv := newPostServer(t, rec)
	c := testClient(srv.URL, "test-token")

	review := schemas.GitHubReview{Body: "b", Event: "REQUEST_CHANGES", Comments: []schemas.GitHubComment{}}
	_, err := c.PostReview(context.Background(), "o", "r", 7, review, "")
	if err == nil {
		t.Fatal("expected 422 error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.StatusCode != 422 {
		t.Errorf("status = %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "own pull request") {
		t.Errorf("body does not carry the 422 reason: %q", apiErr.Body)
	}
}

// ---- App-JWT ---------------------------------------------------------------

func TestGenerateAppJWT_Claims(t *testing.T) {
	privPEM, pub := genRSAKeyPEM(t)
	const appID = "1234567"

	before := time.Now().Unix()
	tokStr, err := generateAppJWT(appID, privPEM)
	if err != nil {
		t.Fatalf("generateAppJWT: %v", err)
	}
	after := time.Now().Unix()

	parsed, err := jwt.Parse(tokStr, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return pub, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("verify JWT with public key: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T", parsed.Claims)
	}

	if iss, _ := claims["iss"].(string); iss != appID {
		t.Errorf("iss = %q, want %q", iss, appID)
	}
	iatF, ok := claims["iat"].(float64)
	if !ok {
		t.Fatalf("iat missing/wrong type: %v", claims["iat"])
	}
	expF, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp missing/wrong type: %v", claims["exp"])
	}
	iat, exp := int64(iatF), int64(expF)
	if iat < before-60 || iat > after-60 {
		t.Errorf("iat = %d, want in [%d,%d] (now-60)", iat, before-60, after-60)
	}
	if exp < before+600 || exp > after+600 {
		t.Errorf("exp = %d, want in [%d,%d] (now+600)", exp, before+600, after+600)
	}
	if exp-iat != 660 {
		t.Errorf("exp-iat = %d, want 660", exp-iat)
	}
}

func TestGenerateAppJWT_BadKey(t *testing.T) {
	if _, err := generateAppJWT("1", "not-a-pem-key"); err == nil {
		t.Fatal("expected error for invalid private key")
	}
}

// ---- installation-token caching -------------------------------------------

func newTokenServer(t *testing.T, instID int, expiresAt string, instCalls, tokCalls *int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/installation", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*instCalls++
		mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("installation lookup missing Bearer JWT: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": instID})
	})
	mux.HandleFunc(fmt.Sprintf("/app/installations/%d/access_tokens", instID), func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*tokCalls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installtoken", "expires_at": expiresAt})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallationToken_CacheHit(t *testing.T) {
	privPEM, _ := genRSAKeyPEM(t)
	var instCalls, tokCalls int
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	srv := newTokenServer(t, 999, future, &instCalls, &tokCalls)
	c := testAppClient(srv.URL, "42", privPEM)

	for i := 0; i < 2; i++ {
		tok, err := c.getInstallationToken(context.Background(), "o", "r")
		if err != nil {
			t.Fatalf("getInstallationToken #%d: %v", i, err)
		}
		if tok != "ghs_installtoken" {
			t.Errorf("token = %q", tok)
		}
	}
	// installation id looked up both times; token minted once (cache hit 2nd time)
	if instCalls != 2 {
		t.Errorf("installation lookups = %d, want 2", instCalls)
	}
	if tokCalls != 1 {
		t.Errorf("token mints = %d, want 1 (cache hit)", tokCalls)
	}
}

func TestInstallationToken_NearExpiryBypassesCache(t *testing.T) {
	privPEM, _ := genRSAKeyPEM(t)
	var instCalls, tokCalls int
	// expires within the 5-minute refresh buffer -> cache never used
	near := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	srv := newTokenServer(t, 777, near, &instCalls, &tokCalls)
	c := testAppClient(srv.URL, "42", privPEM)

	for i := 0; i < 2; i++ {
		if _, err := c.getInstallationToken(context.Background(), "o", "r"); err != nil {
			t.Fatalf("getInstallationToken #%d: %v", i, err)
		}
	}
	if tokCalls != 2 {
		t.Errorf("token mints = %d, want 2 (near-expiry token must not be cached)", tokCalls)
	}
}

func TestFetchPR_UsesAppInstallationToken(t *testing.T) {
	privPEM, _ := genRSAKeyPEM(t)
	rec := &ghRec{}

	// One combined server: PR endpoints + App installation-token endpoints, so
	// the installation token is used for the subsequent PR requests with no
	// cross-host redirect (which would strip the Authorization header).
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var instCalls, tokCalls int
	mux := http.NewServeMux()
	registerFetchHandlers(mux, rec)
	mux.HandleFunc("/repos/o/r/installation", func(w http.ResponseWriter, r *http.Request) {
		instCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 555})
	})
	mux.HandleFunc("/app/installations/555/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		tokCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_appfetch", "expires_at": future})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := testAppClient(srv.URL, "42", privPEM)
	data, err := c.FetchPR(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("FetchPR (app auth): %v", err)
	}
	if data.Title != "Add feature" {
		t.Errorf("title = %q", data.Title)
	}
	rec.mu.Lock()
	auth := rec.auth
	rec.mu.Unlock()
	if auth != "Bearer ghs_appfetch" {
		t.Errorf("fetch used auth %q, want installation token Bearer ghs_appfetch", auth)
	}
	if tokCalls == 0 {
		t.Error("installation token was never minted")
	}
}

// ---- NewClient env cascade -------------------------------------------------

func TestNewClient_TokenCascade(t *testing.T) {
	t.Run("explicit_token_wins", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "from-env")
		t.Setenv("GITHUB_APP_ID", "")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		c := NewClient("explicit")
		if c.token != "explicit" {
			t.Errorf("token = %q, want explicit", c.token)
		}
		if c.useAppAuth {
			t.Error("useAppAuth true, want false")
		}
		if c.baseURL != defaultBaseURL {
			t.Errorf("baseURL = %q", c.baseURL)
		}
	})
	t.Run("falls_back_to_GH_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "env-token")
		c := NewClient("")
		if c.token != "env-token" {
			t.Errorf("token = %q, want env-token", c.token)
		}
	})
	t.Run("empty_when_unset", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		c := NewClient("")
		if c.token != "" {
			t.Errorf("token = %q, want empty", c.token)
		}
	})
	t.Run("app_auth_when_both_set", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "123")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "some-pem")
		c := NewClient("")
		if !c.useAppAuth {
			t.Error("useAppAuth false, want true")
		}
	})
	t.Run("no_app_auth_when_one_missing", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "123")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		c := NewClient("")
		if c.useAppAuth {
			t.Error("useAppAuth true with missing private key, want false")
		}
	})
}

// ---- small helpers ---------------------------------------------------------

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
