package orch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	old, set := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if set {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func clearGitTimeoutEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		gitTimeoutEnv,
		cloneTimeoutEnv,
		fetchTimeoutEnv,
		checkoutTimeoutEnv,
		diffTimeoutEnv,
	} {
		unsetTestEnv(t, name)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = original
		_ = reader.Close()
		_ = writer.Close()
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func installSleepingGit(t *testing.T, tmp, operation string) {
	t.Helper()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		fmt.Sprintf("  [ \"$arg\" = %q ] && exec sleep 10\n", operation) +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func makeFilterUpstream(t *testing.T, path string) {
	t.Helper()
	makeUpstream(t, path)
	if err := os.WriteFile(filepath.Join(path, ".gitattributes"), []byte("*.bin filter=controlled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "asset.bin"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "-A")
	git(t, path, "commit", "-qm", "add filtered asset")

	git(t, path, "checkout", "-q", "-b", "_filter-pr", "main")
	if err := os.WriteFile(filepath.Join(path, "asset.bin"), []byte("pr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, path, "commit", "-qam", "update filtered asset")
	sha := git(t, path, "rev-parse", "HEAD")
	git(t, path, "update-ref", "refs/pull/1/head", sha)
	git(t, path, "checkout", "-q", "main")
	git(t, path, "branch", "-qD", "_filter-pr")
}

func TestGitTimeoutNamesAndDefaultsMatchPythonContract(t *testing.T) {
	clearGitTimeoutEnv(t)
	if gitTimeoutEnv != "PR_AF_GIT_TIMEOUT_SECONDS" {
		t.Errorf("umbrella env = %q", gitTimeoutEnv)
	}
	cases := []struct {
		operationEnv string
		wantEnv      string
		defaultValue time.Duration
		wantDefault  time.Duration
	}{
		{cloneTimeoutEnv, "PR_AF_GIT_CLONE_TIMEOUT_SECONDS", cloneTimeout, 600 * time.Second},
		{fetchTimeoutEnv, "PR_AF_GIT_FETCH_TIMEOUT_SECONDS", prFetchTimeout, 600 * time.Second},
		{checkoutTimeoutEnv, "PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", checkoutTimeout, 600 * time.Second},
		{diffTimeoutEnv, "PR_AF_GIT_DIFF_TIMEOUT_SECONDS", diffTimeout, 120 * time.Second},
	}
	for _, tc := range cases {
		if tc.operationEnv != tc.wantEnv {
			t.Errorf("operation env = %q, want %q", tc.operationEnv, tc.wantEnv)
		}
		if tc.defaultValue != tc.wantDefault {
			t.Errorf("%s default = %s, want %s", tc.wantEnv, tc.defaultValue, tc.wantDefault)
		}
		if got := gitTimeout(tc.operationEnv, tc.defaultValue); got != tc.wantDefault {
			t.Errorf("gitTimeout(%s) = %s, want %s", tc.wantEnv, got, tc.wantDefault)
		}
	}
	if fetchAllTimeout != 600*time.Second {
		t.Errorf("reused-workspace fetch default = %s, want 10m", fetchAllTimeout)
	}
}

func TestGitTimeoutUmbrellaAndOperationPrecedence(t *testing.T) {
	clearGitTimeoutEnv(t)
	t.Setenv(gitTimeoutEnv, "17.5")
	for _, tc := range []struct {
		env string
		def time.Duration
	}{
		{cloneTimeoutEnv, cloneTimeout},
		{fetchTimeoutEnv, prFetchTimeout},
		{checkoutTimeoutEnv, checkoutTimeout},
		{diffTimeoutEnv, diffTimeout},
	} {
		if got := gitTimeout(tc.env, tc.def); got != 17500*time.Millisecond {
			t.Errorf("gitTimeout(%s) = %s, want 17.5s", tc.env, got)
		}
	}

	t.Setenv(checkoutTimeoutEnv, "2.25")
	if got := gitTimeout(checkoutTimeoutEnv, checkoutTimeout); got != 2250*time.Millisecond {
		t.Errorf("checkout timeout = %s, want 2.25s", got)
	}
	if got := gitTimeout(fetchTimeoutEnv, prFetchTimeout); got != 17500*time.Millisecond {
		t.Errorf("fetch timeout = %s, want umbrella 17.5s", got)
	}
}

func TestInvalidGitTimeoutFallsBackToDefault(t *testing.T) {
	for _, raw := range []string{"abc", "-5", "0"} {
		t.Run(raw, func(t *testing.T) {
			clearGitTimeoutEnv(t)
			t.Setenv(gitTimeoutEnv, raw)
			if got := gitTimeout(checkoutTimeoutEnv, checkoutTimeout); got != 600*time.Second {
				t.Errorf("gitTimeout(checkout) = %s, want 10m", got)
			}
		})
	}
}

func TestInvalidOperationTimeoutUsesBuiltinNotUmbrella(t *testing.T) {
	clearGitTimeoutEnv(t)
	t.Setenv(gitTimeoutEnv, "9")
	t.Setenv(checkoutTimeoutEnv, "invalid")
	if got := gitTimeout(checkoutTimeoutEnv, checkoutTimeout); got != 600*time.Second {
		t.Errorf("checkout timeout = %s, want built-in 10m", got)
	}
	if got := gitTimeout(fetchTimeoutEnv, prFetchTimeout); got != 9*time.Second {
		t.Errorf("fetch timeout = %s, want umbrella 9s", got)
	}
}

func TestEmptyGitTimeoutIsUnsetWithoutInvalidValueLog(t *testing.T) {
	clearGitTimeoutEnv(t)
	t.Setenv(gitTimeoutEnv, "")
	var got time.Duration
	output := captureStderr(t, func() {
		got = gitTimeout(checkoutTimeoutEnv, checkoutTimeout)
	})
	if got != checkoutTimeout {
		t.Fatalf("empty umbrella timeout = %s, want built-in %s", got, checkoutTimeout)
	}
	if output != "" {
		t.Fatalf("empty timeout logged as invalid: %q", output)
	}
}

func TestEmptyOperationTimeoutFallsThroughToUmbrella(t *testing.T) {
	clearGitTimeoutEnv(t)
	t.Setenv(gitTimeoutEnv, "9")
	t.Setenv(checkoutTimeoutEnv, "")
	var got time.Duration
	output := captureStderr(t, func() {
		got = gitTimeout(checkoutTimeoutEnv, checkoutTimeout)
	})
	if got != 9*time.Second {
		t.Fatalf("empty operation timeout = %s, want umbrella 9s", got)
	}
	if output != "" {
		t.Fatalf("empty timeout logged as invalid: %q", output)
	}
}

func TestCheckoutTimeoutKillsFilterProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("process-group assertion requires a Unix-like system")
	}
	clearGitTimeoutEnv(t)
	t.Setenv(checkoutTimeoutEnv, "2")
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream")
	makeFilterUpstream(t, upstream)
	target := filepath.Join(tmp, "workspace")
	cloneWorkspace(t, upstream, target)
	git(t, target, "checkout", "-q", "main")
	pidFile := filepath.Join(tmp, "slow-filter.pid")
	filterCommand := fmt.Sprintf("sh -c 'echo $$ > \"$1\"; sleep 30; cat' sh %q", pidFile)
	git(t, target, "config", "filter.controlled.smudge", filterCommand)

	started := time.Now()
	err := checkoutPRBranch(context.Background(), target, 1)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected checkout timeout")
	}
	if elapsed < 1500*time.Millisecond || elapsed > 8*time.Second {
		t.Fatalf("checkout timeout returned after %s, want about 2s", elapsed)
	}
	for _, lockName := range []string{"index.lock", "shallow.lock"} {
		if _, statErr := os.Stat(filepath.Join(target, ".git", lockName)); !os.IsNotExist(statErr) {
			t.Fatalf("%s remains after checkout timeout: %v", lockName, statErr)
		}
	}

	git(t, target, "config", "filter.controlled.smudge", "cat")
	t.Setenv(checkoutTimeoutEnv, "10")
	if err := checkoutPRBranch(context.Background(), target, 1); err != nil {
		t.Fatalf("checkout after timeout cleanup: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "asset.bin")); got != "pr\n" {
		t.Fatalf("checkout after timeout = %q, want %q", got, "pr\n")
	}

	pidBytes, readErr := os.ReadFile(pidFile)
	if os.IsNotExist(readErr) {
		t.Skip("slow smudge filter did not record its PID")
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	filterPID, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse slow smudge filter PID: %v", parseErr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("kill", "-0", strconv.Itoa(filterPID)).Run() != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("slow smudge filter process %d survived git timeout", filterPID)
}

func TestCheckoutSmudgeReceivesLFSSetting(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operator   *string
		wantOutput string
	}{
		{name: "default", wantOutput: "SKIP=1\n"},
		{name: "empty is unset", operator: stringPointer(""), wantOutput: "SKIP=1\n"},
		{name: "operator override", operator: stringPointer("0"), wantOutput: "SKIP=0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearGitTimeoutEnv(t)
			if tc.operator == nil {
				unsetTestEnv(t, "GIT_LFS_SKIP_SMUDGE")
			} else {
				t.Setenv("GIT_LFS_SKIP_SMUDGE", *tc.operator)
			}
			tmp := t.TempDir()
			upstream := filepath.Join(tmp, "upstream")
			makeFilterUpstream(t, upstream)
			target := filepath.Join(tmp, "workspace")
			cloneWorkspace(t, upstream, target)
			git(t, target, "config", "filter.controlled.smudge",
				"printf 'SKIP=%s\\n' \"$GIT_LFS_SKIP_SMUDGE\"")

			if err := checkoutPRBranch(context.Background(), target, 1); err != nil {
				t.Fatalf("checkout: %v", err)
			}
			if got := readFile(t, filepath.Join(target, "asset.bin")); got != tc.wantOutput {
				t.Fatalf("smudge output = %q, want %q", got, tc.wantOutput)
			}
		})
	}
}

func TestResolveRepoCloneUsesResolvedTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim is a POSIX shell script")
	}
	clearGitTimeoutEnv(t)
	tmp := t.TempDir()
	installSleepingGit(t, tmp, "clone")
	t.Setenv("PR_AF_WORKDIR", filepath.Join(tmp, "workspaces"))
	t.Setenv(cloneTimeoutEnv, "1")

	started := time.Now()
	_, err := ResolveRepo(context.Background(), "", "https://github.com/owner/repo/pull/1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveRepo clone error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("clone timeout returned after %s, want about 1s", elapsed)
	}
}

func TestResolveRepoFetchUsesResolvedTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim is a POSIX shell script")
	}
	clearGitTimeoutEnv(t)
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "workspaces")
	if err := os.MkdirAll(filepath.Join(workdir, "repo-pr1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	installSleepingGit(t, tmp, "fetch")
	t.Setenv("PR_AF_WORKDIR", workdir)
	t.Setenv(fetchTimeoutEnv, "1")

	started := time.Now()
	_, err := ResolveRepo(context.Background(), "", "https://github.com/owner/repo/pull/1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveRepo fetch error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("fetch timeout returned after %s, want about 1s", elapsed)
	}
}

func stringPointer(value string) *string {
	return &value
}
