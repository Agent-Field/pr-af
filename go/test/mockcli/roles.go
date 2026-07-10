package main

import (
	"fmt"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// roles.go builds a deterministic, schema-valid result for each PR-AF harness
// (opencode) reasoner. Every builder returns a value whose JSON key set matches
// the reasoner's private harness-result struct (reasoners/schemas.go), so the
// harness runner parses it into T without falling back to seeded defaults. Only
// the reasoners that go through the opencode HARNESS appear here; the four .ai()
// touchpoints (intake gate, coverage gate, merge-gate, polish) never invoke this
// binary — see the run.sh header for why the e2e is not fully offline.

// capitalize upper-cases the first rune of s (ASCII lens names only).
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// defaultBudget mirrors the pydantic BudgetAllocation defaults the harness would
// otherwise seed, so an emitted ReviewDimension carries a realistic budget.
func defaultBudget() schemas.BudgetAllocation {
	return schemas.BudgetAllocation{
		MaxCostUSD:          0.5,
		MaxDurationSeconds:  60,
		MaxReferenceFollows: 3,
		MaxChildSpawns:      2,
	}
}

// ---- anatomy_phase ----

// mockAnatomy is the _AnatomySemanticResult shape (the deterministic diff fields
// are filled by the reasoner itself; the harness only supplies these five).
type mockAnatomy struct {
	PrNarrative      string   `json:"pr_narrative"`
	RiskSurfaces     []string `json:"risk_surfaces"`
	UnrelatedChanges []string `json:"unrelated_changes"`
	IntentGaps       []string `json:"intent_gaps"`
	ContextNotes     string   `json:"context_notes"`
}

func roleAnatomy(prompt string) any {
	files := changedFilesFrom(prompt)
	narrative := "Mock structural analysis: the change introduces new behavior in the touched files."
	if len(files) > 0 {
		narrative = fmt.Sprintf("Mock structural analysis of %s.", strings.Join(files, ", "))
	}
	return mockAnatomy{
		PrNarrative:      narrative,
		RiskSurfaces:     []string{"error handling", "input validation"},
		UnrelatedChanges: []string{},
		IntentGaps:       []string{},
		ContextNotes:     "Mock context notes.",
	}
}

// ---- meta_semantic / meta_mechanical / meta_systemic ----

// roleMeta emits one review dimension for the given lens, targeting the changed
// files parsed from the meta context blob, so the orchestrator has a concrete
// dimension to fan out to review_dimension.
func roleMeta(prompt, lens string) any {
	files := changedFilesFrom(prompt)
	if len(files) == 0 {
		files = []string{"main.go"}
	}
	dim := schemas.ReviewDimension{
		ID:           lens + "-primary",
		Name:         capitalize(lens) + ": core behavior",
		ReviewPrompt: fmt.Sprintf("Investigate the %s correctness of the changes in %s.", lens, strings.Join(files, ", ")),
		TargetFiles:  files,
		ContextFiles: []string{},
		Priority:     5,
		Budget:       defaultBudget(),
	}
	return schemas.MetaDimensionResult{
		Lens:       lens,
		Dimensions: []schemas.ReviewDimension{dim},
		Confidence: 0.8,
		Rationale:  "Mock " + lens + " lens: one focused dimension over the changed files.",
	}
}

// ---- review_dimension (normal + coverage-gap variant) ----

type mockReviewFindings struct {
	Findings   []schemas.ReviewFinding `json:"findings"`
	SubReviews []any                   `json:"sub_reviews"`
}

func roleReviewDimension(prompt string, sc Scenario) any {
	target := "main.go"
	if tf := targetFilesFrom(prompt); len(tf) > 0 {
		target = tf[0]
	} else if cf := changedFilesFrom(prompt); len(cf) > 0 {
		target = cf[0]
	}
	line := sc.LineStart
	if line <= 0 {
		line = 1
	}
	sevs := sc.ReviewFindingSeverities
	if len(sevs) == 0 {
		sevs = []string{"important", "suggestion"}
	}
	findings := make([]schemas.ReviewFinding, 0, len(sevs))
	for i, sev := range sevs {
		findings = append(findings, schemas.ReviewFinding{
			DimensionID:   "mock-review",
			DimensionName: "Mock Reviewer",
			FilePath:      target,
			LineStart:     line,
			LineEnd:       line,
			HunkContext:   "",
			Severity:      schemas.Severity(sev),
			Title:         fmt.Sprintf("Mock finding %d in %s", i+1, target),
			Body:          fmt.Sprintf("**Mock %s finding**: the added code in `%s` needs attention.", sev, target),
			Suggestion:    nil,
			Evidence:      fmt.Sprintf("Step 1: %s line %d introduces new behavior. Step 2: it is not guarded. Step 3: this can fail.", target, line),
			Confidence:    sc.Confidence,
			Tags:          []string{"correctness"},
		})
	}
	return mockReviewFindings{Findings: findings, SubReviews: []any{}}
}

// ---- evidence_verifier ----

type mockVerifiedFinding struct {
	Title             string  `json:"title"`
	Verified          bool    `json:"verified"`
	ActualBehavior    string  `json:"actual_behavior"`
	RevisedSeverity   string  `json:"revised_severity"`
	RevisedConfidence float64 `json:"revised_confidence"`
	VerificationNotes string  `json:"verification_notes"`
}

type mockVerification struct {
	VerifiedFindings []mockVerifiedFinding `json:"verified_findings"`
}

func roleEvidenceVerifier(prompt string) any {
	fs := findingsFrom(prompt)
	out := make([]mockVerifiedFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, mockVerifiedFinding{
			Title:             f.Title,
			Verified:          true,
			ActualBehavior:    "Mock verification: the ground truth supports the finding.",
			RevisedSeverity:   "",
			RevisedConfidence: 0.8,
			VerificationNotes: "Mock verifier confirmed the finding against the extracted code.",
		})
	}
	return mockVerification{VerifiedFindings: out}
}

// ---- adversary_phase ----

type mockAdversary struct {
	Results []schemas.AdversaryResult `json:"results"`
}

func roleAdversary(prompt string, sc Scenario) any {
	fs := findingsFrom(prompt)
	verdict := sc.AdversaryVerdict
	if verdict == "" {
		verdict = "confirmed"
	}
	results := make([]schemas.AdversaryResult, 0, len(fs))
	for i, f := range fs {
		reason := "Mock adversary: ground truth is consistent with the claim."
		if i == 0 {
			// "adversary confirms one" — make the first confirmation explicit.
			reason = "Mock adversary CONFIRMED: traced the failure path through the ground-truth code."
		}
		results = append(results, schemas.AdversaryResult{
			FindingTitle:       f.Title,
			Verdict:            verdict,
			Reason:             reason,
			SeverityAdjustment: "none",
			HiddenTrap:         nil,
		})
	}
	logInvocation("adversary_detail", map[string]any{"confirmed": len(results)})
	return mockAdversary{Results: results}
}

// ---- compound_finder_phase ----

type mockCompound struct {
	Findings []any `json:"findings"`
}

func roleCompoundFinder() any {
	// No synthesized compound findings in the deterministic scenario.
	return mockCompound{Findings: []any{}}
}

// ---- post_worthiness_gate / compound_dedup_phase (keep-all) ----

type mockKeepIndices struct {
	KeepIndices []int  `json:"keep_indices"`
	Reasoning   string `json:"reasoning"`
}

func roleKeepAll(prompt, reasoning string) any {
	n := findingCountFrom(prompt)
	idx := make([]int, 0, n)
	for i := 0; i < n; i++ {
		idx = append(idx, i)
	}
	return mockKeepIndices{KeepIndices: idx, Reasoning: reasoning}
}

// ---- deepen_findings ----

type mockDeepen struct {
	Findings []any `json:"findings"`
}

func roleDeepen() any { return mockDeepen{Findings: []any{}} }

// ---- extract_obligations ----

type mockObligations struct {
	Obligations []any `json:"obligations"`
}

func roleObligations() any { return mockObligations{Obligations: []any{}} }

// ---- verify_obligation ----

type mockObligationVerdict struct {
	Holds      bool    `json:"holds"`
	Title      string  `json:"title"`
	Severity   string  `json:"severity"`
	FilePath   string  `json:"file_path"`
	LineStart  int     `json:"line_start"`
	LineEnd    int     `json:"line_end"`
	Body       string  `json:"body"`
	Evidence   string  `json:"evidence"`
	Suggestion *string `json:"suggestion"`
	Confidence float64 `json:"confidence"`
}

func roleVerifyObligation() any {
	// Obligation holds => no new consistency finding is produced.
	return mockObligationVerdict{
		Holds:      true,
		Title:      "",
		Severity:   "important",
		FilePath:   "",
		LineStart:  0,
		LineEnd:    0,
		Body:       "",
		Evidence:   "Mock verifier: read the other location; the obligation holds.",
		Suggestion: nil,
		Confidence: 0.7,
	}
}

// ---- intake fallback (only when the .ai gate returns not-confident) ----

func roleIntakeFallback(prompt string) any {
	langs := []string{"go"}
	return schemas.IntakeResult{
		PrType:       "feature",
		Complexity:   "standard",
		Languages:    langs,
		AreasTouched: []string{"core"},
		RiskSignals:  []string{},
		AIGenerated:  0.0,
		ReviewDepth:  "standard",
		PrSummary:    "Mock intake fallback: a small change to the touched files.",
	}
}

// ---- planning_phase (registered but dead on the live path) ----

func rolePlanning() any {
	return schemas.ReviewPlan{
		Dimensions:    []schemas.ReviewDimension{},
		CrossRefHints: []string{},
		AIAdjusted:    false,
		TotalBudget:   defaultBudget(),
	}
}
