package prompts

import "github.com/Agent-Field/pr-af/go/internal/schemas"

// Go mirrors of the fixture inputs in scripts/gen_golden.py. The Go builders are
// driven with these and compared against the committed testdata/*.txt fixtures
// the generator produced from the real Python builders.

const fixtureRepo = "/tmp/pr-af-fixture-repo"

// bigFiller mirrors gen_golden.py's big(seed, n).
func bigFiller(seed string, n int) string {
	line := seed + " lorem ipsum dolor sit amet consectetur adipiscing elit. "
	reps := (n / len(line)) + 1
	out := ""
	for i := 0; i < reps; i++ {
		out += line
	}
	return string([]rune(out)[:n]) // n < len; byte==rune here (ASCII)
}

func intakeFix(mut func(*schemas.IntakeResult)) schemas.IntakeResult {
	in := schemas.IntakeResult{
		PrType:       "feature",
		Complexity:   "standard",
		Languages:    []string{"python"},
		AreasTouched: []string{"api"},
		RiskSignals:  []string{"changes API surface or request/response behavior"},
		AIGenerated:  0.0,
		ReviewDepth:  "standard",
		PrSummary:    "Adds a retry wrapper around the HTTP client.",
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func clusterFix(id, name string, files []string, lang string) schemas.ChangeCluster {
	if lang == "" {
		lang = "python"
	}
	return schemas.ChangeCluster{ID: id, Name: name, Files: files, PrimaryLanguage: lang, Description: "desc"}
}

func anatomyFix(mut func(*schemas.AnatomyResult)) schemas.AnatomyResult {
	an := schemas.AnatomyResult{
		Files:            []schemas.FileChange{},
		Clusters:         []schemas.ChangeCluster{},
		BlastRadius:      []string{},
		DependencyGraph:  map[string][]string{},
		Stats:            schemas.DiffStats{},
		RiskSurfaces:     []string{},
		UnrelatedChanges: []string{},
		IntentGaps:       []string{},
	}
	if mut != nil {
		mut(&an)
	}
	return an
}

// anatA mirrors gen_golden.py's anatA (the richly-populated anatomy).
func anatA() schemas.AnatomyResult {
	return anatomyFix(func(a *schemas.AnatomyResult) {
		a.Clusters = []schemas.ChangeCluster{
			clusterFix("cluster_0", "Client core", []string{"client.py"}, ""),
			clusterFix("cluster_1", "Tests", []string{"client.test.ts"}, "typescript"),
		}
		a.RiskSurfaces = []string{"error propagation to callers", "retry storm under load"}
		a.PrNarrative = "Introduces a retry decorator around the HTTP client call path."
		a.BlastRadius = []string{"caller.py"}
		a.IntentGaps = []string{"description mentions backoff but code uses fixed sleep"}
		a.UnrelatedChanges = []string{"README typo fix"}
		a.ContextNotes = "Retry count is read from env."
	})
}

func findingFix(mut func(*schemas.ReviewFinding)) schemas.ReviewFinding {
	f := schemas.ReviewFinding{
		DimensionID:   "d1",
		DimensionName: "Retry semantics",
		FilePath:      "client.py",
		LineStart:     10,
		LineEnd:       12,
		Severity:      "important",
		Title:         "Retry loop can spin forever",
		Body:          "The loop never decrements the counter.",
		Evidence:      "Step 1: caller invokes retry() with n=3.",
		Confidence:    0.7,
		Tags:          []string{"correctness"},
	}
	if mut != nil {
		mut(&f)
	}
	return f
}

func strptr(s string) *string { return &s }
