package prompts

import (
	"strings"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func TestMergeGateGolden(t *testing.T) {
	assertGolden(t, "merge_gate_system", MergeGateSystem)
	base := schemas.ScoredFinding{
		ID: "f_001", DimensionID: "d1", DimensionName: "Retry semantics",
		FilePath: "client.py", LineStart: 10, LineEnd: 12,
		Severity: "important", Title: "Retry loop can spin forever",
		Body: "The loop never decrements the counter.", Confidence: 0.73,
	}
	a := base
	a.Evidence = "Trace: A->B->C."
	a.Suggestion = strptr("Decrement the counter.")
	assertGolden(t, "merge_gate_user_A", MergeGateUserPrompt(a))
	assertGolden(t, "merge_gate_user_B", MergeGateUserPrompt(base))
}

func TestPolishGolden(t *testing.T) {
	assertGolden(t, "polish_system", PolishSystem)
	assertGolden(t, "polish_user_A", PolishUserPrompt("> [!CAUTION] **Must-fix before merge.**\n\nThe retry loop never terminates. See `client.py:10`."))
	assertGolden(t, "polish_user_B", PolishUserPrompt("Minor: rename `x` to `retries`."))
}

func TestGapGolden(t *testing.T) {
	assertGolden(t, "gap_dimension_A", CoverageGapPrompt("The config loader that reads RETRIES was not reviewed."))
	assertGolden(t, "gap_dimension_B", CoverageGapPrompt("Untested error path."))
}

func TestVerifyObligationGolden(t *testing.T) {
	assertGolden(t, "verify_obligation_A", VerifyObligationPrompt("client.py:10 store(key)", "the loader that reads these keys", "the store key equals the lookup key"))
	assertGolden(t, "verify_obligation_B", VerifyObligationPrompt("a.py:1 f()", "def of f", "f is pure"))
}

func TestPostWorthinessGolden(t *testing.T) {
	pw := []schemas.ScoredFinding{
		{Severity: "critical", FilePath: "a.py", LineStart: 3, Title: "Null deref", Body: bigFiller("body", 400), Evidence: bigFiller("ev", 250)},
		{Severity: "nitpick", FilePath: "b.py", LineStart: 9, Title: "Rename var", Body: "cosmetic", Evidence: ""},
		{Severity: "important", FilePath: "c.py", LineStart: 1, Title: "Race", Body: "shared state", Evidence: "two goroutines"},
	}
	assertGolden(t, "post_worthiness_A", PostWorthinessPrompt(pw))
	assertGolden(t, "post_worthiness_B", PostWorthinessPrompt(pw[:2]))
}

func TestCompoundDedupGolden(t *testing.T) {
	cd := []schemas.ReviewFinding{
		{Title: "Shared retry gap", Severity: "important", FilePath: "a.py", Tags: []string{"correctness", "compound"}, Body: bigFiller("bd", 600), Evidence: bigFiller("ed", 400)},
		{Title: "Retry storm", Severity: "critical", FilePath: "b.py", Tags: []string{}, Body: "storm", Evidence: "load"},
	}
	assertGolden(t, "compound_dedup_A", CompoundDedupPrompt(cd, "- Unbounded retry loop\n- Backoff ignored"))
	assertGolden(t, "compound_dedup_B", CompoundDedupPrompt(cd, ""))
}

func TestCompoundFinderGolden(t *testing.T) {
	f1 := findingFix(func(f *schemas.ReviewFinding) { f.Title = "Unbounded retry loop"; f.Tags = []string{"correctness"} })
	f2 := findingFix(func(f *schemas.ReviewFinding) {
		f.Title = "Backoff ignored"
		f.FilePath = "retry.py"
		f.LineStart = 5
		f.Tags = []string{"performance"}
		f.Suggestion = strptr("Sleep between attempts.")
	})
	f3 := findingFix(func(f *schemas.ReviewFinding) {
		f.Title = "Error type swallowed"
		f.FilePath = "errors.py"
		f.Severity = "critical"
		f.Suggestion = strptr("Re-raise original.")
	})
	ev := map[string]*OMap{
		"Unbounded retry loop": omap(
			"primary_code", "def retry(): ...",
			"import_context", "import time",
			"caller_snippets", []string{"client.call()"},
			"related_code", "config.RETRIES",
			"cross_ref_snippets", []string{"x=1"},
		),
	}
	assertGolden(t, "compound_finder_A", CompoundFinderPrompt([]schemas.ReviewFinding{f1, f2, f3}, "", ev))
	assertGolden(t, "compound_finder_B", CompoundFinderPrompt([]schemas.ReviewFinding{f1, f2}, "", nil))

	bigf := []schemas.ReviewFinding{
		findingFix(func(f *schemas.ReviewFinding) { f.Title = strings.Repeat("A", 10); f.Body = bigFiller("b", 5000) }),
		findingFix(func(f *schemas.ReviewFinding) { f.Title = strings.Repeat("B", 10); f.Body = bigFiller("c", 5000) }),
		findingFix(func(f *schemas.ReviewFinding) { f.Title = "Cc"; f.Body = bigFiller("d", 5000) }),
	}
	assertGolden(t, "compound_finder_C", CompoundFinderPrompt(bigf, fixtureRepo, nil))
}

func TestEvidenceVerifierGolden(t *testing.T) {
	evpk := map[string]*OMap{
		"Retry loop can spin forever": omap(
			"primary_code", "while True: try()",
			"caller_snippets", []string{"client.call()"},
			"diff_hunk", "@@ -1 +1 @@",
			"import_context", "import time",
			"related_code", "config",
			"cross_ref_snippets", []string{"r=1"},
		),
	}
	assertGolden(t, "evidence_verifier_A", EvidenceVerifierPrompt([]schemas.ReviewFinding{findingFix(nil)}, evpk, "PR adds retry.", ""))
	assertGolden(t, "evidence_verifier_B", EvidenceVerifierPrompt([]schemas.ReviewFinding{findingFix(nil)}, nil, "", ""))
	assertGolden(t, "evidence_verifier_C", EvidenceVerifierPrompt(
		[]schemas.ReviewFinding{findingFix(func(f *schemas.ReviewFinding) { f.Body = bigFiller("bd", 13000) })}, nil, "", fixtureRepo))
}

func TestAdversaryGolden(t *testing.T) {
	advpk := map[string]*OMap{
		"Retry loop can spin forever": omap(
			"primary_code", "while True: try()",
			"caller_snippets", []string{"client.call()"},
			"diff_hunk", "@@ -1 +1 @@",
			"import_context", "import time",
			"related_code", "config",
		),
	}
	assertGolden(t, "adversary_A", AdversaryPrompt([]schemas.ReviewFinding{findingFix(nil)}, 0.7, "PR adds retry.", "", advpk))
	assertGolden(t, "adversary_B", AdversaryPrompt([]schemas.ReviewFinding{findingFix(nil)}, 0.0, "", "", nil))
	assertGolden(t, "adversary_C", AdversaryPrompt(
		[]schemas.ReviewFinding{findingFix(func(f *schemas.ReviewFinding) { f.Body = bigFiller("bd", 11000) })}, 0.3, "", fixtureRepo, nil))
}

func TestDeepenGolden(t *testing.T) {
	dp := []StrPair{{Key: "client.py", Val: "@@ -1,2 +1,4 @@\n+def retry():\n+    return call()"}}
	assertGolden(t, "deepen_A", DeepenFindingsPrompt(dp, []string{"Unbounded retry loop", "Backoff ignored"}, "", "PR adds retry."))
	assertGolden(t, "deepen_B", DeepenFindingsPrompt(dp, nil, "", ""))
	assertGolden(t, "deepen_C", DeepenFindingsPrompt([]StrPair{{Key: "client.py", Val: bigFiller("hunk", 9500)}}, nil, fixtureRepo, ""))
}

func TestObligationsGolden(t *testing.T) {
	dp := []StrPair{{Key: "client.py", Val: "@@ -1,2 +1,4 @@\n+def retry():\n+    return call()"}}
	assertGolden(t, "obligations_A", ExtractObligationsPrompt(dp, "", "PR adds retry."))
	assertGolden(t, "obligations_B", ExtractObligationsPrompt(dp, "", ""))
	assertGolden(t, "obligations_C", ExtractObligationsPrompt([]StrPair{{Key: "client.py", Val: bigFiller("hunk", 9500)}}, fixtureRepo, ""))
}

func TestReviewDimensionGolden(t *testing.T) {
	assertGolden(t, "review_dimension_A", ReviewDimensionPrompt(ReviewDimensionOptions{
		ReviewPrompt:      "Verify the retry decorator preserves error types raised by the wrapped call.",
		TargetFiles:       []string{"client.py", "retry.py"},
		ContextFiles:      []string{"errors.py"},
		RepoPath:          "",
		CurrentDepth:      0,
		MaxDepth:          2,
		PrNarrative:       "Adds a retry decorator.",
		RiskSurfaces:      []string{"error propagation", "timeout handling"},
		IntakeSummary:     "Feature PR touching the HTTP client.",
		PrDescription:     "Retries are fail-soft by design because callers have their own fallback.",
		DiffPatches:       map[string]string{"client.py": "@@ -1 +1 @@\n-x\n+y", "retry.py": "@@ -2 +2 @@\n-a\n+b"},
		AllDimensionNames: []string{"Semantic: error paths", "Mechanical: signatures"},
		ReviewerFeedback:  "drop nitpicks, focus on correctness",
		PrimedCode:        "1: def retry(fn):\n2:     return fn",
	}))
	assertGolden(t, "review_dimension_B", ReviewDimensionPrompt(ReviewDimensionOptions{
		ReviewPrompt: "Verify the retry decorator preserves error types raised by the wrapped call.",
		TargetFiles:  []string{"client.py", "retry.py"},
		RepoPath:     "",
		CurrentDepth: 2,
		MaxDepth:     2,
	}))
	assertGolden(t, "review_dimension_C", ReviewDimensionPrompt(ReviewDimensionOptions{
		ReviewPrompt:      "Verify retry preserves error types.",
		TargetFiles:       []string{"client.py"},
		ContextFiles:      []string{"errors.py"},
		RepoPath:          fixtureRepo,
		CurrentDepth:      0,
		MaxDepth:          2,
		PrNarrative:       "Adds retry.",
		RiskSurfaces:      []string{"error propagation"},
		IntakeSummary:     "Feature PR.",
		DiffPatches:       map[string]string{"client.py": bigFiller("hunk", 6500)},
		AllDimensionNames: []string{"Semantic"},
		PrimedCode:        bigFiller("code", 6500),
	}))
}
