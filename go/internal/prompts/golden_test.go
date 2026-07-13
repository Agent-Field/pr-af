package prompts

import (
	"embed"
	"fmt"
	"testing"
)

//go:embed testdata/*.txt
var goldenFS embed.FS

// golden loads a committed fixture (produced by scripts/gen_golden.py from the
// real Python builders).
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := goldenFS.ReadFile("testdata/" + name + ".txt")
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// assertGolden compares got against the named fixture byte-for-byte and, on
// mismatch, reports the first differing byte offset with a ±40-byte window on
// each side so the divergence is easy to locate.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want := golden(t, name)
	if got == want {
		return
	}
	off := firstDiff(got, want)
	t.Errorf("golden %s: mismatch at byte %d (got %d bytes, want %d bytes)\n  got : %s\n  want: %s",
		name, off, len(got), len(want), window(got, off), window(want, off))
}

// firstDiff returns the index of the first differing byte, or the length of the
// shorter string when one is a prefix of the other.
func firstDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// window renders a ±40-byte slice of s centered on off, with a caret at the
// diff point, using %q so control chars are visible.
func window(s string, off int) string {
	lo := off - 40
	if lo < 0 {
		lo = 0
	}
	hi := off + 40
	if hi > len(s) {
		hi = len(s)
	}
	return fmt.Sprintf("...%q<HERE>%q...", s[lo:off], s[off:hi])
}

func TestIntakeGolden(t *testing.T) {
	assertGolden(t, "intake_gate_system", IntakeGateSystem)

	assertGolden(t, "intake_ai_A", IntakeGatePrompt(
		"Add retry logic to HTTP client",
		"Wraps the client in a retry decorator with exponential backoff.\nCloses #42.",
		[]string{"enhancement", "backend"}, "alice", 4,
		[]string{"markdown", "python", "typescript"},
		[]string{"feat: add retry", "test: cover retry", "docs: note retry", "chore: lint", "fix: typo", "extra"},
	))
	assertGolden(t, "intake_fallback_A", IntakeFallbackPrompt(
		"Add retry logic to HTTP client",
		"Wraps the client in a retry decorator with exponential backoff.\nCloses #42.",
		"deep", []string{"markdown", "python", "typescript"}, 4,
	))

	assertGolden(t, "intake_ai_B", IntakeGatePrompt(
		"Bump dep", "", nil, "", 0, nil, nil,
	))
	assertGolden(t, "intake_fallback_B", IntakeFallbackPrompt(
		"Bump dep", "", "standard", nil, 0,
	))
}
