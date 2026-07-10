// Package hitl provides PR-AF's human-in-the-loop substrate: the hax REST
// client (hax_client.go) and the pause/approval seam (pause.go).
//
// It is the Go port of src/pr_af/hitl/client.py — the hax client builder, the
// control-plane webhook-URL resolver, and the watchdog-safe create-request
// wrapper. The PR-AF review gate that renders the "pr-af-review-v1" template
// and drives the pause loop lives in review_gate.go (a separate task) and
// consumes this substrate.
package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// HaxCreateRequestTimeout is the hard cap on a single hax create-request call.
//
// Ports pr_af/hitl/client.py::HAX_CREATE_REQUEST_TIMEOUT_SECONDS (120s). The
// hax REST service is synchronous and has occasionally wedged for tens of
// minutes in production; an explicit client-side timeout keeps a hung hax from
// silently burning the parent reasoner's active-time budget.
const HaxCreateRequestTimeout = 120 * time.Second

// DefaultHaxBaseURL mirrors pr_af/hitl/client.py::_DEFAULT_HAX_BASE, which
// defaults HAX_SDK_URL to http://localhost:3000 (same default SWE-AF uses).
const DefaultHaxBaseURL = "http://localhost:3000"

// HaxClient is a thin REST client for the hax human-input service.
//
// The Python port uses the hax-sdk Python client; Go calls the same REST
// surface directly: POST {BaseURL}/api/v1/requests with a Bearer token and a
// camelCase JSON body. The client itself carries no sender identity — the
// sender attribution is resolved from the environment on each CreateRequest,
// matching pr_af/hitl/client.py::create_hax_form_request_with_timeout.
type HaxClient struct {
	// BaseURL is the hax service origin WITHOUT the /api/v1 suffix (appended by
	// CreateRequest). Default: DefaultHaxBaseURL. Python stores base_url WITH
	// the /api/v1 suffix; the resulting wire URL is identical either way.
	BaseURL string
	// APIKey is the hax API key sent as "Authorization: Bearer <key>".
	APIKey string
	// HTTPClient is used for all requests; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Timeout overrides HaxCreateRequestTimeout when non-zero (tests use this to
	// exercise the watchdog with sub-second durations).
	Timeout time.Duration
}

// BuildHaxClientFromEnv constructs a HaxClient from HAX_API_KEY / HAX_SDK_URL.
//
// Returns nil when HAX_API_KEY is unset or blank — callers MUST treat a nil
// client as "HITL disabled" and post the review directly. This is the on/off
// switch (ports pr_af/hitl/client.py::build_hax_client_from_env returning None).
//
// Note: unlike SWE-AF's Go client, sender identity is NOT read here — Python's
// build_hax_client_from_env reads only HAX_API_KEY / HAX_SDK_URL; HAX_SENDER_*
// is resolved at CreateRequest time.
func BuildHaxClientFromEnv() *HaxClient {
	apiKey := strings.TrimSpace(os.Getenv("HAX_API_KEY"))
	if apiKey == "" {
		return nil
	}
	// os.environ.get("HAX_SDK_URL", default): the default applies only when the
	// var is ABSENT; an explicitly empty value is kept (LookupEnv preserves that
	// distinction, unlike SWE-AF's Go which coerces empty -> default).
	base, ok := os.LookupEnv("HAX_SDK_URL")
	if !ok {
		base = DefaultHaxBaseURL
	}
	return &HaxClient{
		BaseURL: strings.TrimRight(base, "/"),
		APIKey:  apiKey,
	}
}

// resolveSender reproduces the sender attribution built by
// pr_af/hitl/client.py::create_hax_form_request_with_timeout:
//
//	_sender_name = os.getenv("HAX_SENDER_NAME") or os.getenv("NODE_ID", "pr-af")
//	key          = os.getenv("HAX_SENDER_KEY", _sender_name)
//
// The Python `or` treats an empty HAX_SENDER_NAME as falsy (falls through to
// NODE_ID), whereas os.getenv("NODE_ID", "pr-af") / os.getenv("HAX_SENDER_KEY",
// default) only substitute the default when the variable is ABSENT (an empty
// value is kept). LookupEnv preserves that absent-vs-empty distinction.
func resolveSender() (key, displayName string) {
	displayName = os.Getenv("HAX_SENDER_NAME")
	if displayName == "" {
		if v, ok := os.LookupEnv("NODE_ID"); ok {
			displayName = v
		} else {
			displayName = "pr-af"
		}
	}
	if v, ok := os.LookupEnv("HAX_SENDER_KEY"); ok {
		key = v
	} else {
		key = displayName
	}
	return key, displayName
}

// CreateRequestParams are the inputs to CreateRequest. Each maps to a camelCase
// body key exactly as the hax-sdk Python client builds it (hax/client.py::
// create_request). The sender is resolved from the environment inside
// CreateRequest and is not part of these params.
type CreateRequestParams struct {
	Type             string         // -> "type" (e.g. "pr-af-review-v1")
	Payload          map[string]any // -> "payload"
	Title            string         // -> "title" (omitted when empty)
	Description      *string        // -> "description" (omitted when nil)
	WebhookURL       string         // -> "webhookUrl" (omitted when empty)
	ExpiresInSeconds int            // -> "expiresInSeconds" (omitted when <= 0)
	UserID           string         // -> "userId" (omitted when empty)
	Metadata         map[string]any // -> "metadata" (omitted when nil)
}

// CreatedRequest is the subset of the hax create-request response we consume.
type CreatedRequest struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreateRequest POSTs {BaseURL}/api/v1/requests with a Bearer token and a
// camelCase JSON body, bounded by a hard timeout (HaxCreateRequestTimeout, or
// HaxClient.Timeout when set). A timeout surfaces as an error rather than a
// silent multi-hour stall.
//
// The body always carries a "sender" object ({key, displayName}) resolved from
// the environment (resolveSender) — matching Python's create wrapper, and
// diverging from SWE-AF's Go client, which never sends sender attribution.
// publicKey is intentionally omitted so hax returns plaintext response values
// end-to-end (the Python client is built without a public key).
func (c *HaxClient) CreateRequest(ctx context.Context, p CreateRequestParams) (*CreatedRequest, error) {
	body := map[string]any{
		"type":    p.Type,
		"payload": p.Payload,
	}
	if p.Title != "" {
		body["title"] = p.Title
	}
	if p.Description != nil {
		body["description"] = *p.Description
	}
	if p.WebhookURL != "" {
		body["webhookUrl"] = p.WebhookURL
	}
	if p.ExpiresInSeconds > 0 {
		body["expiresInSeconds"] = p.ExpiresInSeconds
	}
	if p.UserID != "" {
		body["userId"] = p.UserID
	}
	if p.Metadata != nil {
		body["metadata"] = p.Metadata
	}
	senderKey, senderName := resolveSender()
	body["sender"] = map[string]any{
		"key":         senderKey,
		"displayName": senderName,
	}
	// publicKey is intentionally omitted (Python HaxClient is built without one,
	// so end-to-end response encryption is disabled and values come back plain).

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("hax create_request: marshal body: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = HaxCreateRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := c.BaseURL + "/api/v1/requests"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("hax create_request: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf(
				"hax create_request (%s) timed out after %s; hax-sdk is likely wedged: %w",
				p.Type, timeout, err,
			)
		}
		return nil, fmt.Errorf("hax create_request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hax create_request: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hax create_request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var created CreatedRequest
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("hax create_request: decode response: %w", err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("hax create_request: response missing id: %s", strings.TrimSpace(string(respBody)))
	}
	return &created, nil
}

// ApprovalWebhookURL resolves the control-plane webhook URL the CP calls back
// when a human responds to a paused review. Ports
// pr_af/hitl/client.py::approval_webhook_url with its three-tier precedence:
//
//	AGENTFIELD_PUBLIC_URL (env)  >  agentFieldServer (the agent's server)  >  AGENTFIELD_SERVER (env)
//
// AGENTFIELD_PUBLIC_URL wins because the callback is invoked by hax-sdk, which
// lives in a separate Railway project and must reach a publicly routable URL —
// not the internal AGENTFIELD_SERVER address the agent uses to call the CP.
// The caller passes the agent's own server URL (Python's app.agentfield_server)
// as agentFieldServer. Returns nil when no base can be resolved (Python returns
// None), matching the design's *string signature (SWE-AF's Go returned "").
func ApprovalWebhookURL(agentFieldServer string) *string {
	base := os.Getenv("AGENTFIELD_PUBLIC_URL")
	if base == "" {
		base = agentFieldServer
	}
	if base == "" {
		base = os.Getenv("AGENTFIELD_SERVER")
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	url := base + "/api/v1/webhooks/approval-response"
	return &url
}
