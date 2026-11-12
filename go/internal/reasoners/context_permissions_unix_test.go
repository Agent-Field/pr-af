//go:build unix

package reasoners

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Run alone: the process umask is shared state, including on macOS.
func TestWriteContextFileOwnerOnly(t *testing.T) {
	// Keep umask changes in a dedicated process even when CI runs all tests.
	if os.Getenv("PR_AF_CONTEXT_MODE_TEST") != "child" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWriteContextFileOwnerOnly$", "-test.timeout=5s")
		cmd.Env = append(os.Environ(), "PR_AF_CONTEXT_MODE_TEST=child")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated permission check: %v\n%s", err, output)
		}
		return
	}

	previous := syscall.Umask(0)
	defer syscall.Umask(previous)
	for _, existing := range []bool{false, true} {
		name := "fresh"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			dir := filepath.Join(repo, ".pr-af-context")
			path := filepath.Join(dir, "context.json")
			if existing {
				if err := os.Mkdir(dir, 0777); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("previous synthetic context"), 0666); err != nil {
					t.Fatal(err)
				}
			}
			got, err := writeContextFile("synthetic context", "context.json", repo)
			if err != nil {
				t.Fatal(err)
			}
			if got != path {
				t.Fatal("context path changed")
			}
			for _, check := range []struct {
				path string
				mode os.FileMode
			}{{dir, 0700}, {path, 0600}} {
				info, err := os.Stat(check.path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != check.mode {
					t.Errorf("context permissions=%#o, want %#o", info.Mode().Perm(), check.mode)
				}
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "synthetic context" {
				t.Fatal("context content changed")
			}
		})
	}
}

func TestWriteContextFileReplacesExistingFileLink(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(repo, "outside")
	if err := os.WriteFile(outside, []byte("outside context"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".pr-af-context")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "context.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := writeContextFile("new synthetic context", "context.json", repo); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside context" {
		t.Error("existing context link target was modified")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Error("context link was not replaced with an owner-only regular file")
	}
}

func TestWriteContextFileSecuresLinkedDirectory(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Chmod(outside, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".pr-af-context")); err != nil {
		t.Fatal(err)
	}
	path, err := writeContextFile("synthetic context", "context.json", repo)
	if err != nil {
		t.Fatal("existing linked context directory no longer works")
	}
	if path != filepath.Join(repo, ".pr-af-context", "context.json") {
		t.Error("linked context path changed")
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{outside, 0700}, {filepath.Join(outside, "context.json"), 0600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != check.mode {
			t.Error("linked context target is not owner-only")
		}
	}
}
