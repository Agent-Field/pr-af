// Package github is a small REST client for the GitHub API used by the PR-AF
// review pipeline: fetch a PR (metadata + changed files + commits + unified
// diff), and post a review with inline comments.
//
// It is a faithful port of src/pr_af/github/client.py. Behavioural contract
// (byte-exact with Python):
//   - ParsePRURL: regexp ^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+).
//   - FetchPR: 4 attempts, sleep 2.0*(attempt+1)s between them, retry on
//     transport errors + 5xx + 403/429, fail fast on any other 4xx.
//   - Per-request timeouts: 30s (fetch), 15s (installation token), 60s (post).
//   - Redirects followed (Go's default http.Client) — matches httpx
//     follow_redirects=True on the fetch client (see divergence notes below).
//   - Pagination of /files and /commits at per_page=100.
//   - Diff via Accept: application/vnd.github.v3.diff.
//   - Auth: PAT (constructor arg or GH_TOKEN) OR GitHub App (RS256 JWT +
//     cached installation tokens) when GITHUB_APP_ID and
//     GITHUB_APP_PRIVATE_KEY are both set.
//   - PostReview surfaces HTTP failures (incl. 422 "own pull request") as a
//     typed *APIError carrying StatusCode + Body so the orchestrator can
//     detect the 422 and downgrade the event to COMMENT.
//
// Divergences from Python (intentional, behaviourally inert in production):
//   - The installation-token endpoints are routed through the client's
//     baseURL instead of a hardcoded https://api.github.com, so tests can
//     point them at an httptest server. In production baseURL IS
//     https://api.github.com, so behaviour is identical.
//   - Go's http.Client follows redirects for every request; Python leaves
//     follow_redirects at its default (off) for the installation-token and
//     post-review calls and only enables it for fetch. Those endpoints never
//     redirect on api.github.com, so this is inert.
//   - The installation-token cache is guarded by a mutex (Python's asyncio
//     single-thread has none); FetchPR/PostReview may run concurrently in Go.
//   - Debug print() lines from the Python client are dropped (they are stdout
//     diagnostics, not behaviour).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

const defaultBaseURL = "https://api.github.com"

const postReviewMaxAttempts = 3

var prURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// APIError is returned for any GitHub response with status >= 400. It carries
// the status code and raw response body so callers can classify failures — in
// particular the orchestrator inspects StatusCode==422 with Body containing
// "own pull request" to downgrade the review event to COMMENT and retry.
type APIError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api error: status=%d url=%s body=%s", e.StatusCode, e.URL, e.Body)
}

// transportError wraps a connection/read/timeout failure (anything returned by
// http.Client.Do or reading the response body). It mirrors httpx.TransportError
// so the FetchPR retry loop can distinguish retryable transport failures from
// non-retryable local errors (e.g. JSON decode), which Python does not catch.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// Client is the GitHub surface the review pipeline depends on.
type Client interface {
	ParsePRURL(url string) (owner, repo string, number int, err error)
	FetchPR(ctx context.Context, owner, repo string, number int) (schemas.GitHubPRData, error)
	PostReview(ctx context.Context, owner, repo string, number int, review schemas.GitHubReview, commitID string) (map[string]any, error)
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

type client struct {
	token         string
	appID         string
	appPrivateKey string
	useAppAuth    bool

	baseURL    string
	httpClient *http.Client

	cacheMu    sync.Mutex
	tokenCache map[int]cachedToken

	// sleepFn overrides the inter-attempt backoff sleep (tests set a no-op).
	// nil => real ctx-aware sleep.
	sleepFn func(ctx context.Context, d time.Duration) error
}

var _ Client = (*client)(nil)

// NewClient builds a GitHub client. Auth is resolved at construction, matching
// Python's per-instance _use_app_auth:
//   - PAT: the token argument, or GH_TOKEN when the argument is empty.
//   - GitHub App: used when GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY are both
//     set; the installation token then takes precedence over the PAT.
func NewClient(token string) *client {
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	appID := os.Getenv("GITHUB_APP_ID")
	appKey := os.Getenv("GITHUB_APP_PRIVATE_KEY")
	return &client{
		token:         token,
		appID:         appID,
		appPrivateKey: appKey,
		useAppAuth:    isGitHubAppConfigured(appID, appKey),
		baseURL:       defaultBaseURL,
		httpClient:    &http.Client{},
		tokenCache:    map[int]cachedToken{},
	}
}

// ParsePRURL extracts owner, repo and PR number from a GitHub PR URL.
func (c *client) ParsePRURL(url string) (owner, repo string, number int, err error) {
	m := prURLRe.FindStringSubmatch(url)
	if m == nil {
		return "", "", 0, fmt.Errorf("Invalid GitHub PR URL: %s", url)
	}
	n, convErr := strconv.Atoi(m[3])
	if convErr != nil {
		return "", "", 0, fmt.Errorf("Invalid GitHub PR URL: %s", url)
	}
	return m[1], m[2], n, nil
}

// sleep waits d, honouring ctx cancellation. Overridable via sleepFn for tests.
func (c *client) sleep(ctx context.Context, d time.Duration) error {
	if c.sleepFn != nil {
		return c.sleepFn(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// request performs a single HTTP request with a per-call timeout, returning the
// response body. On status >= 400 it returns (*APIError). On a transport/read
// failure it returns (*transportError). jsonBody, when non-nil, is JSON-encoded
// as the request body with Content-Type: application/json.
func (c *client) request(ctx context.Context, method, rawurl string, headers map[string]string, jsonBody any, timeout time.Duration) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, rawurl, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &transportError{err: err}
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, &transportError{err: readErr}
	}
	if resp.StatusCode >= 400 {
		return data, &APIError{StatusCode: resp.StatusCode, Body: string(data), URL: rawurl}
	}
	return data, nil
}

// generateAppJWT is defined in jwt.go.

// isGitHubAppConfigured reports whether both GitHub App credentials are present.
func isGitHubAppConfigured(appID, key string) bool {
	return appID != "" && key != ""
}

// getInstallationToken exchanges the App JWT for a scoped installation token
// for owner/repo. The installation id is always looked up first; the token
// itself is cached until 5 minutes before expiry.
func (c *client) getInstallationToken(ctx context.Context, owner, repo string) (string, error) {
	appJWT, err := generateAppJWT(c.appID, c.appPrivateKey)
	if err != nil {
		return "", err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + appJWT,
		"Accept":        "application/vnd.github+json",
	}

	// Find the installation for this repo.
	instBody, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/installation", c.baseURL, owner, repo),
		headers, nil, 15*time.Second)
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(instBody, &inst); err != nil {
		return "", err
	}
	installationID := inst.ID

	// Cache check (5 min buffer before expiry).
	c.cacheMu.Lock()
	if ct, ok := c.tokenCache[installationID]; ok {
		if time.Now().Before(ct.expiresAt.Add(-5 * time.Minute)) {
			c.cacheMu.Unlock()
			return ct.token, nil
		}
	}
	c.cacheMu.Unlock()

	// Mint a fresh installation token.
	tokBody, err := c.request(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID),
		headers, nil, 15*time.Second)
	if err != nil {
		return "", err
	}
	var tokResp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(tokBody, &tokResp); err != nil {
		return "", err
	}
	expiresAt, err := time.Parse(time.RFC3339, tokResp.ExpiresAt)
	if err != nil {
		return "", err
	}

	c.cacheMu.Lock()
	c.tokenCache[installationID] = cachedToken{token: tokResp.Token, expiresAt: expiresAt}
	c.cacheMu.Unlock()
	return tokResp.Token, nil
}

// headersForRepo builds request headers, preferring a GitHub App installation
// token over the PAT.
func (c *client) headersForRepo(ctx context.Context, owner, repo string) (map[string]string, error) {
	headers := map[string]string{"Accept": "application/vnd.github+json"}
	if c.useAppAuth {
		token, err := c.getInstallationToken(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		headers["Authorization"] = "Bearer " + token
	} else if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}
	return headers, nil
}

// FetchPR fetches PR metadata, changed files, commit messages and the unified
// diff, retrying transient failures.
func (c *client) FetchPR(ctx context.Context, owner, repo string, number int) (schemas.GitHubPRData, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		data, err := c.fetchPROnce(ctx, owner, repo, number)
		if err == nil {
			return data, nil
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			s := apiErr.StatusCode
			if s >= 400 && s < 500 && s != 403 && s != 429 {
				return schemas.GitHubPRData{}, err // fail fast on other 4xx
			}
			// 5xx / 403 / 429 => retryable
		} else {
			var te *transportError
			if !errors.As(err, &te) {
				// Not an HTTP status error and not a transport error (e.g. a
				// JSON decode error). Python never catches these, so propagate
				// immediately rather than retrying.
				return schemas.GitHubPRData{}, err
			}
			// transport => retryable
		}

		lastErr = err
		if serr := c.sleep(ctx, time.Duration(2*(attempt+1))*time.Second); serr != nil {
			return schemas.GitHubPRData{}, serr
		}
	}
	return schemas.GitHubPRData{}, lastErr
}

// prMetadataResp mirrors the fields read from the PR-metadata JSON.
type prMetadataResp struct {
	Title  string  `json:"title"`
	Body   *string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type fileResp struct {
	Filename         string  `json:"filename"`
	Status           *string `json:"status"` // pointer: absent => default "modified"
	Additions        int     `json:"additions"`
	Deletions        int     `json:"deletions"`
	Patch            string  `json:"patch"`
	PreviousFilename *string `json:"previous_filename"`
}

type commitResp struct {
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

func (c *client) fetchPROnce(ctx context.Context, owner, repo string, number int) (schemas.GitHubPRData, error) {
	authHeaders, err := c.headersForRepo(ctx, owner, repo)
	if err != nil {
		return schemas.GitHubPRData{}, err
	}

	// 1. PR metadata.
	metaBody, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.baseURL, owner, repo, number),
		authHeaders, nil, 30*time.Second)
	if err != nil {
		return schemas.GitHubPRData{}, err
	}
	var meta prMetadataResp
	if err := json.Unmarshal(metaBody, &meta); err != nil {
		return schemas.GitHubPRData{}, err
	}

	// 2. Changed files (paginated at per_page=100).
	changedFiles := []schemas.ChangedFile{}
	for page := 1; ; page++ {
		filesBody, err := c.request(ctx, http.MethodGet,
			fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", c.baseURL, owner, repo, number, page),
			authHeaders, nil, 30*time.Second)
		if err != nil {
			return schemas.GitHubPRData{}, err
		}
		var filesPage []fileResp
		if err := json.Unmarshal(filesBody, &filesPage); err != nil {
			return schemas.GitHubPRData{}, err
		}
		if len(filesPage) == 0 {
			break
		}
		for _, f := range filesPage {
			status := "modified"
			if f.Status != nil {
				status = *f.Status
			}
			changedFiles = append(changedFiles, schemas.ChangedFile{
				Path:         f.Filename,
				Status:       status,
				Additions:    f.Additions,
				Deletions:    f.Deletions,
				Patch:        f.Patch,
				PreviousPath: f.PreviousFilename,
			})
		}
		if len(filesPage) < 100 {
			break
		}
	}

	// 3. Commit messages (paginated at per_page=100).
	commitMessages := []string{}
	for page := 1; ; page++ {
		commitsBody, err := c.request(ctx, http.MethodGet,
			fmt.Sprintf("%s/repos/%s/%s/pulls/%d/commits?per_page=100&page=%d", c.baseURL, owner, repo, number, page),
			authHeaders, nil, 30*time.Second)
		if err != nil {
			return schemas.GitHubPRData{}, err
		}
		var commitsData []commitResp
		if err := json.Unmarshal(commitsBody, &commitsData); err != nil {
			return schemas.GitHubPRData{}, err
		}
		if len(commitsData) == 0 {
			break
		}
		for _, cm := range commitsData {
			if cm.Commit.Message != "" {
				commitMessages = append(commitMessages, cm.Commit.Message)
			}
		}
		if len(commitsData) < 100 {
			break
		}
	}

	// 4. Unified diff (same endpoint, diff Accept header).
	diffHeaders := make(map[string]string, len(authHeaders))
	for k, v := range authHeaders {
		diffHeaders[k] = v
	}
	diffHeaders["Accept"] = "application/vnd.github.v3.diff"
	diffBody, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.baseURL, owner, repo, number),
		diffHeaders, nil, 30*time.Second)
	if err != nil {
		return schemas.GitHubPRData{}, err
	}

	labels := []string{}
	for _, l := range meta.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	description := ""
	if meta.Body != nil {
		description = *meta.Body
	}

	return schemas.GitHubPRData{
		Owner:          owner,
		Repo:           repo,
		Number:         number,
		Title:          meta.Title,
		Description:    description,
		Labels:         labels,
		Author:         meta.User.Login,
		BaseSHA:        meta.Base.SHA,
		HeadSHA:        meta.Head.SHA,
		CommitMessages: commitMessages,
		Diff:           string(diffBody),
		ChangedFiles:   changedFiles,
	}, nil
}

// PostReview posts a review with inline comments to a PR. Comments are included
// only when their original list is non-empty, filtered to those with a path and
// a positive line. On any HTTP failure (including the 422 "own pull request"
// case the orchestrator downgrades) it returns a typed *APIError.
func (c *client) PostReview(ctx context.Context, owner, repo string, number int, review schemas.GitHubReview, commitID string) (map[string]any, error) {
	payload := map[string]any{
		"body":  review.Body,
		"event": review.Event,
	}
	if commitID != "" {
		payload["commit_id"] = commitID
	}
	if len(review.Comments) > 0 {
		comments := make([]map[string]any, 0, len(review.Comments))
		for _, cm := range review.Comments {
			if cm.Path != "" && cm.Line > 0 {
				comments = append(comments, map[string]any{
					"path": cm.Path,
					"line": cm.Line,
					"side": cm.Side,
					"body": cm.Body,
				})
			}
		}
		payload["comments"] = comments
	}

	authHeaders, err := c.headersForRepo(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.baseURL, owner, repo, number)

	var lastErr error
	for attempt := 0; attempt < postReviewMaxAttempts; attempt++ {
		body, err := c.request(ctx, http.MethodPost, endpoint, authHeaders, payload, 60*time.Second)
		if err == nil {
			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if !isRetryablePostReviewError(err) {
			return nil, err
		}

		lastErr = err
		if attempt == postReviewMaxAttempts-1 {
			break
		}
		if err := c.sleep(ctx, time.Duration(1<<(attempt+1))*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// isRetryablePostReviewError reports whether a review creation failure is
// transient. Client errors are deliberately excluded so the orchestrator can
// retain ownership of its semantic 422 own-PR fallback.
func isRetryablePostReviewError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500 && apiErr.StatusCode <= 599
	}
	var transportErr *transportError
	return errors.As(err, &transportErr)
}
