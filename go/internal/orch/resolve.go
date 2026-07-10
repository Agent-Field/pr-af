package orch

// resolve.go ports the repo-resolution / PR-branch-checkout code that lives in
// src/pr_af/app.py (_resolve_repo, _checkout_pr_branch, _extract_pr_number) plus
// the orchestrator's _compute_repo_diff. Python's github/client.py::clone_repo
// was deliberately NOT ported into internal/github, so the clone + checkout
// semantics live here and shell out to git directly, tokenizing the remote with
// GH_TOKEN exactly as Python does.
//
// The verbatim error strings (design §B.4) are reproduced for the fetch/checkout
// failures so callers (and tests) see byte-identical messages.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// git subprocess timeouts, matching app.py's subprocess.run(..., timeout=…).
const (
	cloneTimeout    = 600 * time.Second // large repos need time
	fetchAllTimeout = 600 * time.Second // reused workspace refresh
	prFetchTimeout  = 300 * time.Second // fetch of the PR head
	checkoutTimeout = 30 * time.Second
	diffTimeout     = 120 * time.Second
)

// gitEnv reproduces app.py's git_env: the process environment plus
// GIT_TERMINAL_PROMPT=0 and GIT_ASKPASS=echo so a missing credential fails fast
// instead of blocking on an interactive prompt.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
}

// runGit executes a git command with a hard timeout and returns stdout, stderr,
// and the error. dir is passed via -C by the caller (kept out of here so the
// clone command — which has no -C — works too).
func runGit(parent context.Context, timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// ExtractPRNumber ports app.py::_extract_pr_number: the integer following
// "/pull/" in a github.com URL, or (0, false) when absent/unparsable.
func ExtractPRNumber(prURL string) (int, bool) {
	if !strings.Contains(prURL, "github.com") || !strings.Contains(prURL, "/pull/") {
		return 0, false
	}
	tail := prURL[strings.LastIndex(prURL, "/pull/")+len("/pull/"):]
	// split on "/" and strip, matching .split("/")[0].strip("/").
	seg := tail
	if i := strings.Index(seg, "/"); i >= 0 {
		seg = seg[:i]
	}
	seg = strings.Trim(seg, "/")
	n, convErr := strconv.Atoi(seg)
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// checkoutPRBranch ports app.py::_checkout_pr_branch. It fetches the PR head into
// FETCH_HEAD (which always succeeds, even when the workspace is reused and
// pr-review is the current branch) and then checkout -B (re)points pr-review at
// it — the fix for the silent "reused workspace reviews the first PR forever"
// bug. The two failure strings are the §B.4 verbatim contracts.
func checkoutPRBranch(ctx context.Context, targetDir string, prNumber int) error {
	_, stderr, err := runGit(ctx, prFetchTimeout,
		"-C", targetDir, "fetch", "--depth", "1", "origin", fmt.Sprintf("pull/%d/head", prNumber))
	if err != nil {
		return fmt.Errorf("git fetch of PR #%d head failed: %s", prNumber, strings.TrimSpace(stderr))
	}
	_, stderr, err = runGit(ctx, checkoutTimeout,
		"-C", targetDir, "checkout", "-B", "pr-review", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("git checkout of PR #%d (pr-review) failed: %s", prNumber, strings.TrimSpace(stderr))
	}
	return nil
}

// ResolveRepo ports app.py::_resolve_repo. It resolves repoPath / prURL to a
// local directory: an existing dir is returned as-is (resolved absolute), an
// http(s)/git@ URL is (shallow) cloned into $PR_AF_WORKDIR (with GH_TOKEN
// injected into a github.com HTTPS remote) and the PR branch checked out when a
// PR number is known, and anything else falls back to $PR_AF_REPO_PATH / cwd.
//
// Empty strings stand in for Python's None. Errors mirror Python's ValueError
// ("git clone failed: …", plus the checkout strings via checkoutPRBranch).
func ResolveRepo(ctx context.Context, repoPath, prURL string) (string, error) {
	workdir := os.Getenv("PR_AF_WORKDIR")
	if workdir == "" {
		workdir = "/workspaces"
	}
	target := repoPath
	prNumber := 0
	hasPR := false

	if target == "" && strings.Contains(prURL, "github.com") && strings.Contains(prURL, "/pull/") {
		// parts = pr_url.split("github.com/")[-1].split("/pull/")[0].strip("/")
		afterHost := prURL[strings.LastIndex(prURL, "github.com/")+len("github.com/"):]
		parts := afterHost
		if i := strings.Index(parts, "/pull/"); i >= 0 {
			parts = parts[:i]
		}
		parts = strings.Trim(parts, "/")
		if strings.Count(parts, "/") == 1 {
			target = fmt.Sprintf("https://github.com/%s.git", parts)
		}
		prNumber, hasPR = ExtractPRNumber(prURL)
	}

	// Existing directory → return resolved absolute path.
	if target != "" && isDir(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return target, nil
		}
		return abs, nil
	}

	// Remote URL → clone (or refresh) into the workspace.
	if target != "" && (strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "git@")) {
		repoName := strings.TrimSuffix(lastSegment(strings.TrimRight(target, "/")), ".git")
		targetDir := filepath.Join(workdir, repoName)
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return "", fmt.Errorf("git clone failed: %s", strings.TrimSpace(err.Error()))
		}

		cloneURL := target
		ghToken := os.Getenv("GH_TOKEN")
		if ghToken != "" && strings.HasPrefix(cloneURL, "https://github.com/") {
			cloneURL = strings.Replace(cloneURL, "https://github.com/",
				fmt.Sprintf("https://%s@github.com/", ghToken), 1)
		}

		if isDir(targetDir) && isDir(filepath.Join(targetDir, ".git")) {
			// Reused workspace: refresh all refs (errors swallowed, as Python does).
			_, _, _ = runGit(ctx, fetchAllTimeout, "-C", targetDir, "fetch", "--all")
		} else {
			cloneCmd := []string{"clone", "--depth", "1", "--no-tags", cloneURL, targetDir}
			if hasPR && prNumber != 0 {
				// Skip default-branch checkout; the PR ref is fetched next.
				cloneCmd = []string{"clone", "--depth", "1", "--no-tags", "--no-checkout", cloneURL, targetDir}
			}
			_, stderr, err := runGit(ctx, cloneTimeout, cloneCmd...)
			if err != nil {
				return "", fmt.Errorf("git clone failed: %s", strings.TrimSpace(stderr))
			}
		}

		if hasPR && prNumber != 0 {
			if err := checkoutPRBranch(ctx, targetDir, prNumber); err != nil {
				return "", err
			}
		}
		return targetDir, nil
	}

	// Fallback: PR_AF_REPO_PATH or cwd.
	fallback := os.Getenv("PR_AF_REPO_PATH")
	if fallback == "" {
		if cwd, err := os.Getwd(); err == nil {
			fallback = cwd
		}
	}
	abs, err := filepath.Abs(fallback)
	if err != nil {
		return fallback, nil
	}
	return abs, nil
}

// computeRepoDiff ports orchestrator._compute_repo_diff: a `git diff` over a
// revision range derived from base/head refs. A non-zero exit is a ValueError in
// Python → wrapped in ErrBadInput here (the review()-caught 400 class).
func computeRepoDiff(ctx context.Context, repoPath, baseRef, headRef string) (string, error) {
	if headRef != "" && baseRef == "" {
		baseRef = "HEAD"
	}
	var revision string
	switch {
	case baseRef != "" && headRef != "":
		revision = fmt.Sprintf("%s...%s", baseRef, headRef)
	case baseRef != "":
		revision = fmt.Sprintf("%s...HEAD", baseRef)
	default:
		revision = "HEAD~1...HEAD"
	}
	stdout, stderr, err := runGit(ctx, diffTimeout, "-C", repoPath, "diff", "--no-color", revision)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "Failed to compute git diff"
		}
		return "", fmt.Errorf("%w: %s", ErrBadInput, msg)
	}
	return stdout, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
