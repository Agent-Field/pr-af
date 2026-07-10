//go:build functional

package functional

import (
	"net/http"
	"testing"
)

// TestHealth asserts the Go node serves /health with a 200 (design §F). By the
// time TestMain returns, readiness has already been polled, so this is a direct
// single-shot assertion.
//
// Contract: GET :8007/health -> 200.
func TestHealth(t *testing.T) {
	requireStack(t)

	body, status, err := httpGet(prafBaseURL + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v", prafBaseURL, err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET %s/health -> %d, want 200 (body: %s)", prafBaseURL, status, string(body))
	}
}
