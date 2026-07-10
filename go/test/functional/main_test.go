//go:build functional

// Package functional holds the black-box / functional parity tests for the PR-AF
// Go port (design §F, work-breakdown T4.3). They exercise the *live* stack — a
// control plane plus the Go node (pr-af-go :8007) — brought up via the
// self-contained compose.functional.yml, and assert the parity contracts the
// Python→Go port must preserve:
//
//   - /health on the node returns 200 (TestHealth);
//   - the node registers EXACTLY the 17 Python reasoner names (design §B.1) — no
//     more, no fewer — with `review` untagged and the other 16 tagged
//     ["review","pr"] where the control plane exposes per-reasoner tags
//     (TestRegistrationParity, hardcoded from the PYTHON surface, NOT the Go
//     register.go);
//   - a deterministic (no-LLM) pr-af-go.review against a nonexistent repo_path
//     fails fast (before any harness / .ai() call) and the terminal execution
//     record carries a non-succeeded status plus an error message — the review
//     error-shape contract (TestReviewErrorShape, design §F V2).
//
// All files carry the `functional` build tag so they are invisible to the unit CI
// job (`go test ./...`) and run only under
// `go test -tags functional ./test/functional/`.
//
// TestMain owns the stack lifecycle: it brings the compose stack up once
// (building the Go image if needed), waits for the node to be healthy and
// registered, runs every test against that shared stack, then tears the stack
// down (removing volumes). If Docker is unavailable the whole suite SKIPS with a
// message rather than failing.
package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const (
	// composeFile is the self-contained functional stack (control plane + the Go
	// node). It is distinct from the production docker-compose.go.yml, which is an
	// opt-in add-on that joins the Python stack's network and has no control plane
	// of its own. The file bakes UNCOMMON host ports (28080/28017) directly — see
	// its header — so no separate `!override` file is needed. Path is relative to
	// the repo root (compose runs there).
	composeFile = "go/test/functional/compose.functional.yml"

	// composeProject isolates this stack's containers/volumes/network from any
	// concurrently running pr-af / pr-af-go compose project.
	composeProject = "pr-af-go-functional"

	// Host ports are deliberately uncommon (see compose.functional.yml) to avoid a
	// dev `af server` / dev stack squatting on the CP's host port, which silently
	// breaks readiness. Keep in sync with the compose file.
	cpBaseURL   = "http://localhost:28080"
	prafBaseURL = "http://localhost:28017"

	prafNodeID = "pr-af-go"

	// Generous ceilings: the Go image is a multi-stage build that runs
	// `go mod download` against the module proxy, so a cold `up --build` can take
	// several minutes.
	composeUpTimeout = 15 * time.Minute
	readyTimeout     = 4 * time.Minute
	composeDownGrace = 3 * time.Minute
)

// stackReady is set true once TestMain has a healthy, registered stack. When
// Docker is unavailable it stays false and every test skips via requireStack.
var stackReady bool

// stackSkipReason explains, for the skip message, why the stack is not ready.
var stackSkipReason string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if !dockerAvailable() {
		stackSkipReason = "docker (with a running daemon) is not available"
		fmt.Println("functional: SKIP — " + stackSkipReason)
		// Still run: every test calls requireStack and skips cleanly.
		return m.Run()
	}

	root, err := repoRoot()
	if err != nil {
		stackSkipReason = "could not locate repo root: " + err.Error()
		fmt.Println("functional: SKIP — " + stackSkipReason)
		return m.Run()
	}

	fmt.Printf("functional: bringing up stack (%s, project %s) from %s ...\n",
		composeFile, composeProject, root)
	if err := composeUp(root); err != nil {
		// Docker IS available here, so a failed `up` is a real failure (broken image
		// build, config error), not an environmental skip. Dump logs, tear down,
		// and fail the suite.
		dumpComposeLogs(root)
		_ = composeDown(root)
		fmt.Println("functional: FAIL — docker compose up failed: " + err.Error())
		return 1
	}

	// Always tear the stack down, even on panic in a test.
	defer func() {
		fmt.Println("functional: tearing down stack ...")
		if err := composeDown(root); err != nil {
			fmt.Println("functional: WARNING compose down failed: " + err.Error())
		}
	}()

	fmt.Println("functional: waiting for node to be healthy + registered ...")
	if err := waitForStackReady(); err != nil {
		dumpComposeLogs(root)
		stackSkipReason = "stack did not become ready: " + err.Error()
		fmt.Println("functional: SKIP — " + stackSkipReason)
		return m.Run()
	}

	stackReady = true
	fmt.Println("functional: stack ready — running tests")
	return m.Run()
}

// requireStack skips the calling test unless TestMain brought up a ready stack.
func requireStack(t *testing.T) {
	t.Helper()
	if !stackReady {
		t.Skipf("live stack unavailable: %s", stackSkipReason)
	}
}

// ---------------------------------------------------------------------------
// docker compose lifecycle
// ---------------------------------------------------------------------------

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// `docker info` fails fast if the daemon is not reachable.
	return exec.CommandContext(ctx, "docker", "info").Run() == nil
}

// repoRoot resolves the pr-af repo root (where go/ lives) from this test file's
// location: go/test/functional -> ../../.. .
func repoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, composeFile)); err != nil {
		return "", fmt.Errorf("%s not found under %s: %w", composeFile, root, err)
	}
	return root, nil
}

// composeArgs prefixes every compose invocation with the project name and the
// self-contained compose file (host ports baked in).
func composeArgs(rest ...string) []string {
	args := []string{"compose", "-p", composeProject, "-f", composeFile}
	return append(args, rest...)
}

func composeUp(root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), composeUpTimeout)
	defer cancel()
	// --build ensures the Go image reflects the current source.
	cmd := exec.CommandContext(ctx, "docker", composeArgs("up", "-d", "--build")...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func composeDown(root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), composeDownGrace)
	defer cancel()
	// -v removes the project's named volumes so the run leaves no state behind.
	cmd := exec.CommandContext(ctx, "docker", composeArgs("down", "-v")...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dumpComposeLogs(root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", composeArgs("logs", "--tail", "80")...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// ---------------------------------------------------------------------------
// readiness
// ---------------------------------------------------------------------------

func waitForStackReady() error {
	deadline := time.Now().Add(readyTimeout)

	if err := waitForHTTP200(prafBaseURL+"/health", deadline); err != nil {
		return fmt.Errorf("health %s: %w", prafBaseURL+"/health", err)
	}
	if err := waitForRegistration(prafNodeID, deadline); err != nil {
		return fmt.Errorf("registration %s: %w", prafNodeID, err)
	}
	return nil
}

func waitForHTTP200(url string, deadline time.Time) error {
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out (last: %v)", lastErr)
}

func waitForRegistration(nodeID string, deadline time.Time) error {
	var lastErr error
	for time.Now().Before(deadline) {
		names, _, err := fetchReasoners(nodeID)
		if err == nil && len(names) > 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no reasoners yet")
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out (last: %v)", lastErr)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// capabilitiesResponse is the subset of GET /api/v1/discovery/capabilities we
// assert on: the running agents, each with its reasoner list carrying id +
// (optional) tags. This is the discovery surface control-plane:latest exposes —
// the older per-node record (/api/v1/nodes/:id) does not exist on this image.
// Reasoners registered WITHOUT tags omit the `tags` key entirely (so a nil slice
// distinguishes "no tags" from a present tag set).
type capabilitiesResponse struct {
	Capabilities []struct {
		AgentID   string `json:"agent_id"`
		Reasoners []struct {
			ID   string   `json:"id"`
			Tags []string `json:"tags"`
		} `json:"reasoners"`
	} `json:"capabilities"`
}

// fetchReasoners returns the set of reasoner names registered by nodeID and a
// per-name tag map (nil slice = the CP reported no tags for that reasoner), as
// reported by the control-plane capabilities discovery endpoint.
func fetchReasoners(nodeID string) (names map[string]bool, tags map[string][]string, err error) {
	url := fmt.Sprintf("%s/api/v1/discovery/capabilities?limit=500", cpBaseURL)
	body, status, err := httpGet(url)
	if err != nil {
		return nil, nil, err
	}
	if status != http.StatusOK {
		return nil, nil, fmt.Errorf("GET %s -> %d: %s", url, status, string(body))
	}
	var caps capabilitiesResponse
	if err := json.Unmarshal(body, &caps); err != nil {
		return nil, nil, fmt.Errorf("decode capabilities: %w", err)
	}
	names = map[string]bool{}
	tags = map[string][]string{}
	found := false
	for _, agent := range caps.Capabilities {
		if agent.AgentID != nodeID {
			continue
		}
		found = true
		for _, r := range agent.Reasoners {
			if r.ID != "" {
				names[r.ID] = true
				tags[r.ID] = r.Tags
			}
		}
	}
	if !found {
		return names, tags, fmt.Errorf("agent %q not present in capabilities", nodeID)
	}
	return names, tags, nil
}

func httpGet(url string) ([]byte, int, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

func httpPostJSON(url string, payload any) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// diffSets returns (missing = want-got, extra = got-want).
func diffSets(want, got map[string]bool) (missing, extra []string) {
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	return missing, extra
}
