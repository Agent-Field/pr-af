package orch

// V8: PR-branch checkout is idempotent — a reused workspace (same PR reviewed
// again) re-points the pr-review branch to the NEW head of that PR, and an
// unfetchable ref surfaces the exact "git fetch of PR #N head failed: …" error.
// ResolveRepo keys workspaces per PR (<repoName>-pr<N>) so concurrent reviews
// of different PRs of the same repo never share a checkout; plain <repoName>
// is kept when no PR number is known. Derived from tests/test_resolve_repo.py.

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

func TestResolveRepoKeysWorkspacePerPR(t *testing.T) {
	// Two different PR numbers of the same repo must resolve to two different
	// workspace directories, each checked out at its own PR head.
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "widgets")
	makeUpstream(t, upstream)
	publishPRRef(t, upstream, 1, "pr1")
	publishPRRef(t, upstream, 2, "pr2")

	workdir := filepath.Join(tmp, "workspaces")
	t.Setenv("PR_AF_WORKDIR", workdir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the per-PR workspaces as clones of the local upstream so
	// ResolveRepo takes the reuse path (fetch + PR checkout against the local
	// origin) instead of cloning from github.com. autocrlf is disabled so the
	// marker assertions below stay byte-exact on Windows checkouts.
	for _, ws := range []string{"widgets-pr1", "widgets-pr2"} {
		dir := filepath.Join(workdir, ws)
		cloneWorkspace(t, upstream, dir)
		git(t, dir, "config", "core.autocrlf", "false")
	}

	dir1, err := ResolveRepo(context.Background(), "", "https://github.com/acme/widgets/pull/1")
	if err != nil {
		t.Fatalf("ResolveRepo(pull/1): %v", err)
	}
	dir2, err := ResolveRepo(context.Background(), "", "https://github.com/acme/widgets/pull/2")
	if err != nil {
		t.Fatalf("ResolveRepo(pull/2): %v", err)
	}

	if dir1 == dir2 {
		t.Fatalf("PR #1 and PR #2 share workspace %q — parallel reviews would collide", dir1)
	}
	if got := filepath.Base(dir1); got != "widgets-pr1" {
		t.Errorf("PR #1 workspace = %q, want widgets-pr1", got)
	}
	if got := filepath.Base(dir2); got != "widgets-pr2" {
		t.Errorf("PR #2 workspace = %q, want widgets-pr2", got)
	}
	if got := readFile(t, filepath.Join(dir1, "marker.txt")); got != "pr1\n" {
		t.Errorf("PR #1 workspace marker = %q, want %q", got, "pr1\n")
	}
	if got := readFile(t, filepath.Join(dir2, "marker.txt")); got != "pr2\n" {
		t.Errorf("PR #2 workspace marker = %q, want %q", got, "pr2\n")
	}
}

func TestResolveRepoPlainKeyWithoutPRNumber(t *testing.T) {
	// repo_path/diff_text flows carry no PR number — the workspace stays keyed
	// by plain <repoName>, preserving the pre-per-PR layout.
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "widgets")
	makeUpstream(t, upstream)

	workdir := filepath.Join(tmp, "workspaces")
	t.Setenv("PR_AF_WORKDIR", workdir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the plain-keyed workspace so no network clone is attempted.
	git(t, tmp, "clone", "--depth", "1", "--no-tags", upstream, filepath.Join(workdir, "widgets"))

	dir, err := ResolveRepo(context.Background(), "https://github.com/acme/widgets.git", "")
	if err != nil {
		t.Fatalf("ResolveRepo(url, no PR): %v", err)
	}
	if got := filepath.Base(dir); got != "widgets" {
		t.Errorf("workspace = %q, want plain widgets", got)
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
