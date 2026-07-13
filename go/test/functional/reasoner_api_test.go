//go:build functional

package functional

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestReviewErrorShape drives the full CP async surface for pr-af-go.review with a
// nonexistent repo_path (design §F V2). A local repo_path that is neither a
// directory nor a clonable URL makes the orchestrator's intake `git diff` fail
// IMMEDIATELY — before any harness (opencode) or .ai() call — so the whole path is
// deterministic and zero-LLM. The contract asserted:
//
//   - the execution reaches a TERMINAL non-succeeded status (failed / error /
//     timeout / cancelled), never "succeeded"; and
//   - the terminal record carries a non-empty error message.
//
// This exercises CP async execute -> node dispatch -> reviewHandler -> orchestrator
// -> §B.4 error mapping -> CP terminal persistence end to end, proving the review
// reasoner is reachable and its failure surfaces with the expected shape.
func TestReviewErrorShape(t *testing.T) {
	requireStack(t)

	input := map[string]any{
		// Guaranteed not a git repo (and not a clonable URL) inside the container.
		"repo_path": "/nonexistent/pr-af-go-functional-probe",
		"dry_run":   true,
	}

	execID := startAsyncReview(t, prafNodeID+".review", input)
	rec := pollTerminal(t, execID, 90*time.Second)

	status := strings.ToLower(strings.TrimSpace(rec.Status))
	switch status {
	case "succeeded", "success", "completed":
		t.Fatalf("review of a nonexistent repo_path unexpectedly succeeded (status=%q, record=%s)",
			rec.Status, rec.raw)
	case "":
		t.Fatalf("execution %s never reported a terminal status (record=%s)", execID, rec.raw)
	}

	if strings.TrimSpace(rec.errorText()) == "" {
		t.Errorf("terminal execution %s has status=%q but no error message (record=%s)",
			execID, rec.Status, rec.raw)
	}
	t.Logf("review error-shape OK: status=%q error=%q", rec.Status, truncate(rec.errorText(), 200))
}

// startAsyncReview POSTs an async execute request and returns the execution_id.
func startAsyncReview(t *testing.T, target string, input map[string]any) string {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/execute/async/%s", cpBaseURL, target)
	body, status, err := httpPostJSON(url, map[string]any{"input": input})
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusCreated {
		t.Fatalf("POST %s -> %d, want 2xx (body: %s)", url, status, string(body))
	}
	var resp struct {
		ExecutionID string `json:"execution_id"`
		WorkflowID  string `json:"workflow_id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode async execute response: %v (body: %s)", err, string(body))
	}
	if resp.ExecutionID == "" {
		t.Fatalf("async execute returned no execution_id (body: %s)", string(body))
	}
	return resp.ExecutionID
}

// executionRecord is the subset of GET /api/v1/executions/:id the test asserts on.
// Error text may arrive under either `error` or `error_message` depending on CP
// version, so both are captured.
type executionRecord struct {
	Status       string `json:"status"`
	Error        string `json:"error"`
	ErrorMessage string `json:"error_message"`
	raw          string
}

func (r executionRecord) errorText() string {
	if r.Error != "" {
		return r.Error
	}
	return r.ErrorMessage
}

// pollTerminal polls the execution record until it reaches a terminal status or
// the deadline elapses.
func pollTerminal(t *testing.T, execID string, within time.Duration) executionRecord {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/executions/%s", cpBaseURL, execID)
	deadline := time.Now().Add(within)
	var last executionRecord
	for time.Now().Before(deadline) {
		body, status, err := httpGet(url)
		if err == nil && status == http.StatusOK {
			var rec executionRecord
			if json.Unmarshal(body, &rec) == nil {
				rec.raw = string(body)
				last = rec
				switch strings.ToLower(strings.TrimSpace(rec.Status)) {
				case "succeeded", "success", "completed", "failed", "error", "timeout", "cancelled", "canceled":
					return rec
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return last
}

// truncate shortens s to n runes for log output.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
