package orch

// V8: PR-branch checkout is idempotent — a reused workspace re-points the
// pr-review branch to the NEW PR head (the regression that previously reviewed
// every PR against the first one), and an unfetchable ref surfaces the exact
// "git fetch of PR #N head failed: …" error. Derived from tests/test_resolve_repo.py.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func makeUpstream(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "-q", "-b", "main")
	git(t, path, "config", "user.email", "test@example.com")
	git(t, path, "config", "user.name", "test")
	git(t, path, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(path, "marker.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "-A")
	git(t, path, "commit", "-qm", "initial")
}

func publishPRRef(t *testing.T, upstream string, prNumber int, content string) {
	t.Helper()
	branch := "_pr" + itoa(prNumber)
	git(t, upstream, "checkout", "-q", "-b", branch, "main")
	if err := os.WriteFile(filepath.Join(upstream, "marker.txt"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, upstream, "commit", "-qam", "pr"+itoa(prNumber))
	sha := git(t, upstream, "rev-parse", "HEAD")
	git(t, upstream, "update-ref", "refs/pull/"+itoa(prNumber)+"/head", sha)
	git(t, upstream, "checkout", "-q", "main")
	git(t, upstream, "branch", "-qD", branch)
}

func cloneWorkspace(t *testing.T, upstream, target string) {
	t.Helper()
	git(t, filepath.Dir(upstream), "clone", "--depth", "1", "--no-tags", "--no-checkout", upstream, target)
}

func TestCheckoutReusedWorkspaceUpdatesToNewPRHead(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream")
	makeUpstream(t, upstream)
	publishPRRef(t, upstream, 1, "pr1")

	target := filepath.Join(tmp, "workspace")
	cloneWorkspace(t, upstream, target)
	marker := filepath.Join(target, "marker.txt")

	if err := checkoutPRBranch(context.Background(), target, 1); err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	if got := readFile(t, marker); got != "pr1\n" {
		t.Fatalf("after PR #1: marker = %q, want %q", got, "pr1\n")
	}
	if got := git(t, target, "rev-parse", "--abbrev-ref", "HEAD"); got != "pr-review" {
		t.Fatalf("branch = %q, want pr-review", got)
	}

	// Second PR arrives; the workspace is reused (pr-review still checked out).
	publishPRRef(t, upstream, 2, "pr2")
	git(t, target, "fetch", "--all") // what ResolveRepo does for a reused dir

	if err := checkoutPRBranch(context.Background(), target, 2); err != nil {
		t.Fatalf("second checkout: %v", err)
	}
	if got := readFile(t, marker); got != "pr2\n" {
		t.Fatalf("regression: after PR #2 marker = %q, want %q (still on PR #1?)", got, "pr2\n")
	}
}

func TestCheckoutFreshWorkspaceReflectsPRHead(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream")
	makeUpstream(t, upstream)
	publishPRRef(t, upstream, 7, "pr7")

	target := filepath.Join(tmp, "workspace")
	cloneWorkspace(t, upstream, target)

	if err := checkoutPRBranch(context.Background(), target, 7); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "marker.txt")); got != "pr7\n" {
		t.Fatalf("marker = %q, want %q", got, "pr7\n")
	}
}

func TestCheckoutRaisesOnUnfetchablePRRef(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream")
	makeUpstream(t, upstream)

	target := filepath.Join(tmp, "workspace")
	cloneWorkspace(t, upstream, target)

	err := checkoutPRBranch(context.Background(), target, 999)
	if err == nil {
		t.Fatal("expected error for unfetchable PR ref")
	}
	if !strings.HasPrefix(err.Error(), "git fetch of PR #999 head failed:") {
		t.Fatalf("error = %q, want prefix %q", err.Error(), "git fetch of PR #999 head failed:")
	}
}

func TestExtractPRNumber(t *testing.T) {
	cases := []struct {
		url  string
		want int
		ok   bool
	}{
		{"https://github.com/o/r/pull/42", 42, true},
		{"https://github.com/o/r/pull/42/files", 42, true},
		{"https://github.com/o/r/issues/42", 0, false},
		{"not a url", 0, false},
	}
	for _, c := range cases {
		got, ok := ExtractPRNumber(c.url)
		if got != c.want || ok != c.ok {
			t.Errorf("ExtractPRNumber(%q) = (%d,%v), want (%d,%v)", c.url, got, ok, c.want, c.ok)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
