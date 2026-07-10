//go:build functional

package functional

import (
	"sort"
	"testing"
)

// The parity checklist (design §B.1, V1): the Go node registers the EXACT 17
// Python reasoner names — `review` (externally driven, NO tags) plus the 16
// in-process router reasoners (each tagged ["review","pr"]). These expectations
// are hardcoded from the PYTHON registration surface (src/pr_af/app.py review() +
// the 16 harness reasoners), NOT read from the Go register.go, so the test fails
// loudly on any drift in either direction.

// reviewName is the single externally-driven reasoner; it carries no tags.
const reviewName = "review"

// internalReasoners are the 16 router reasoners the orchestrator invokes
// in-process but that Python (and the Go port) still CP-register, each tagged
// ["review","pr"]. Order matches design §B.1.
var internalReasoners = []string{
	"intake_phase",
	"anatomy_phase",
	"planning_phase",
	"meta_semantic",
	"meta_mechanical",
	"meta_systemic",
	"review_dimension",
	"compound_finder_phase",
	"post_worthiness_gate",
	"compound_dedup_phase",
	"evidence_verifier",
	"adversary_phase",
	"deepen_findings",
	"extract_obligations",
	"verify_obligation",
	"coverage_gate",
}

// reviewTags is the semantic domain tag set every internal reasoner carries.
var reviewTags = []string{"review", "pr"}

// allReasonerNames is the full 17-name surface.
func allReasonerNames() []string {
	return append([]string{reviewName}, internalReasoners...)
}

// TestRegistrationParity asserts the node exposes EXACTLY the 17 expected reasoner
// names — no more, no fewer — and, where the control plane exposes per-reasoner
// tags, that `review` is untagged and the other 16 carry ["review","pr"].
func TestRegistrationParity(t *testing.T) {
	requireStack(t)

	wantNames := allReasonerNames()
	if len(wantNames) != 17 {
		t.Fatalf("test bug: expected-name list has %d entries, want 17", len(wantNames))
	}

	got, tags, err := fetchReasoners(prafNodeID)
	if err != nil {
		t.Fatalf("fetch reasoners for %s: %v", prafNodeID, err)
	}

	// --- exact name-set parity (V1) ---
	want := make(map[string]bool, len(wantNames))
	for _, n := range wantNames {
		want[n] = true
	}
	missing, extra := diffSets(want, got)
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s reasoner-name parity MISMATCH:\n  missing (expected, not registered): %v\n  extra (registered, not expected): %v\n  got %d names, want 17",
			prafNodeID, missing, extra, len(got))
	}
	if len(got) != 17 {
		t.Errorf("%s registered %d reasoners, want exactly 17", prafNodeID, len(got))
	}

	// --- per-name tag parity (where the CP exposes tags) ---
	// The CP may omit per-reasoner tags from the node record; only assert when at
	// least one reasoner entry carried a non-nil tag slice.
	tagsExposed := false
	for _, ts := range tags {
		if ts != nil {
			tagsExposed = true
			break
		}
	}
	if !tagsExposed {
		t.Logf("control plane does not expose per-reasoner tags in the node record; skipping tag assertions (names verified)")
		return
	}

	// review: no tags.
	if ts := tags[reviewName]; len(ts) != 0 {
		t.Errorf("reasoner %q should have NO tags, got %v", reviewName, ts)
	}
	// the 16 internal reasoners: exactly ["review","pr"] (order-insensitive).
	for _, name := range internalReasoners {
		if !sameStringSet(tags[name], reviewTags) {
			t.Errorf("reasoner %q tags = %v, want %v", name, tags[name], reviewTags)
		}
	}
}

// sameStringSet reports whether a and b contain the same elements (order- and
// duplicate-insensitive).
func sameStringSet(a, b []string) bool {
	as := make(map[string]bool, len(a))
	for _, s := range a {
		as[s] = true
	}
	bs := make(map[string]bool, len(b))
	for _, s := range b {
		bs[s] = true
	}
	if len(as) != len(bs) {
		return false
	}
	for k := range as {
		if !bs[k] {
			return false
		}
	}
	return true
}
