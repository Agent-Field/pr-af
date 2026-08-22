package schemas

import (
	"bytes"
	"encoding/json"
	"testing"
)

// mustUnmarshal decodes data into a fresh T or fails the test.
func mustUnmarshal[T any](t *testing.T, data string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("unmarshal %q into %T: %v", data, v, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// V10 — UnmarshalJSON default-seeding: "{}" seeds the §C.1 non-zero defaults,
// while a present key (even false/0/"") overrides. Derived from the contract.
// ---------------------------------------------------------------------------

func TestReviewInputDefaults(t *testing.T) {
	in := mustUnmarshal[ReviewInput](t, "{}")
	if in.Depth != "auto" {
		t.Errorf("Depth = %q, want auto", in.Depth)
	}
	if in.Focus != "auto" {
		t.Errorf("Focus = %q, want auto", in.Focus)
	}
	if in.OutputFormat != "github" {
		t.Errorf("OutputFormat = %q, want github", in.OutputFormat)
	}
	if in.SuggestionMode != "comment" {
		t.Errorf("SuggestionMode = %q, want comment", in.SuggestionMode)
	}
	if in.MaxReviewDepth != 2 {
		t.Errorf("MaxReviewDepth = %d, want 2", in.MaxReviewDepth)
	}
	// Budget caps stay nil (resolved later by config).
	if in.MaxCostUSD != nil {
		t.Errorf("MaxCostUSD = %v, want nil", *in.MaxCostUSD)
	}
	if in.MaxDurationSeconds != nil {
		t.Errorf("MaxDurationSeconds = %v, want nil", *in.MaxDurationSeconds)
	}
	if in.PrURL != nil {
		t.Errorf("PrURL = %v, want nil", *in.PrURL)
	}
	// list defaults are non-nil empty slices.
	if in.IgnorePaths == nil || len(in.IgnorePaths) != 0 {
		t.Errorf("IgnorePaths = %v, want empty non-nil", in.IgnorePaths)
	}
	if in.Hints == nil || len(in.Hints) != 0 {
		t.Errorf("Hints = %v, want empty non-nil", in.Hints)
	}

	// Present zero/explicit values override the seeded defaults.
	in2 := mustUnmarshal[ReviewInput](t, `{"depth":"deep","max_review_depth":0,"suggestion_mode":"code","dry_run":true}`)
	if in2.Depth != "deep" {
		t.Errorf("override Depth = %q, want deep", in2.Depth)
	}
	if in2.MaxReviewDepth != 0 {
		t.Errorf("override MaxReviewDepth = %d, want 0", in2.MaxReviewDepth)
	}
	if in2.SuggestionMode != "code" {
		t.Errorf("override SuggestionMode = %q, want code", in2.SuggestionMode)
	}
	if !in2.DryRun {
		t.Errorf("override DryRun = false, want true")
	}

	// A present budget cap yields a non-nil pointer to the value.
	in3 := mustUnmarshal[ReviewInput](t, `{"max_cost_usd":1.5,"max_duration_seconds":600}`)
	if in3.MaxCostUSD == nil || *in3.MaxCostUSD != 1.5 {
		t.Errorf("MaxCostUSD = %v, want *1.5", in3.MaxCostUSD)
	}
	if in3.MaxDurationSeconds == nil || *in3.MaxDurationSeconds != 600 {
		t.Errorf("MaxDurationSeconds = %v, want *600", in3.MaxDurationSeconds)
	}
}

func TestBudgetAllocationDefaults(t *testing.T) {
	b := mustUnmarshal[BudgetAllocation](t, "{}")
	if b.MaxCostUSD != 0.5 || b.MaxDurationSeconds != 60 || b.MaxReferenceFollows != 3 || b.MaxChildSpawns != 2 {
		t.Errorf("BudgetAllocation{} = %+v, want {0.5 60 3 2}", b)
	}
	b0 := mustUnmarshal[BudgetAllocation](t, `{"max_cost_usd":0,"max_child_spawns":0}`)
	if b0.MaxCostUSD != 0 || b0.MaxChildSpawns != 0 {
		t.Errorf("override zeros = %+v, want MaxCostUSD=0 MaxChildSpawns=0", b0)
	}
	// Untouched fields keep their seeded defaults.
	if b0.MaxDurationSeconds != 60 || b0.MaxReferenceFollows != 3 {
		t.Errorf("partial override lost other defaults: %+v", b0)
	}
}

func TestReviewDimensionDefaults(t *testing.T) {
	d := mustUnmarshal[ReviewDimension](t, "{}")
	if d.Priority != 1 {
		t.Errorf("Priority = %d, want 1", d.Priority)
	}
	if d.ContextFiles == nil || len(d.ContextFiles) != 0 {
		t.Errorf("ContextFiles = %v, want empty non-nil", d.ContextFiles)
	}
	// Nested Budget is seeded even when absent.
	if d.Budget != defaultBudgetAllocation() {
		t.Errorf("Budget = %+v, want %+v", d.Budget, defaultBudgetAllocation())
	}
	// A partially-specified budget re-seeds the untouched sub-fields.
	d2 := mustUnmarshal[ReviewDimension](t, `{"budget":{"max_cost_usd":1.0},"priority":0}`)
	if d2.Priority != 0 {
		t.Errorf("override Priority = %d, want 0", d2.Priority)
	}
	if d2.Budget.MaxCostUSD != 1.0 || d2.Budget.MaxDurationSeconds != 60 {
		t.Errorf("nested budget = %+v, want MaxCostUSD=1.0 MaxDurationSeconds=60", d2.Budget)
	}
}

func TestSubReviewRequestDefaults(t *testing.T) {
	s := mustUnmarshal[SubReviewRequest](t, "{}")
	if s.Priority != 1 {
		t.Errorf("Priority = %d, want 1", s.Priority)
	}
	if s.ContextFiles == nil || len(s.ContextFiles) != 0 {
		t.Errorf("ContextFiles = %v, want empty non-nil", s.ContextFiles)
	}
}

func TestReviewFindingDefaults(t *testing.T) {
	f := mustUnmarshal[ReviewFinding](t, "{}")
	if f.Severity != "suggestion" {
		t.Errorf("Severity = %q, want suggestion", f.Severity)
	}
	if f.Confidence != 0.5 {
		t.Errorf("Confidence = %v, want 0.5", f.Confidence)
	}
	if f.Tags == nil || len(f.Tags) != 0 {
		t.Errorf("Tags = %v, want empty non-nil", f.Tags)
	}
	if f.Suggestion != nil {
		t.Errorf("Suggestion = %v, want nil", *f.Suggestion)
	}
	// Present zero confidence overrides the 0.5 default.
	f2 := mustUnmarshal[ReviewFinding](t, `{"confidence":0,"severity":"critical","suggestion":"fix"}`)
	if f2.Confidence != 0 {
		t.Errorf("override Confidence = %v, want 0", f2.Confidence)
	}
	if f2.Severity != "critical" {
		t.Errorf("override Severity = %q, want critical", f2.Severity)
	}
	if f2.Suggestion == nil || *f2.Suggestion != "fix" {
		t.Errorf("Suggestion = %v, want *fix", f2.Suggestion)
	}
}

func TestScoredFindingDefaults(t *testing.T) {
	s := mustUnmarshal[ScoredFinding](t, "{}")
	if s.DiffSide != "RIGHT" {
		t.Errorf("DiffSide = %q, want RIGHT", s.DiffSide)
	}
	if s.Severity != "suggestion" {
		t.Errorf("Severity = %q, want suggestion", s.Severity)
	}
	if s.Confidence != 0.5 {
		t.Errorf("Confidence = %v, want 0.5", s.Confidence)
	}
	if s.Tags == nil || s.ActiveMultipliers == nil {
		t.Errorf("Tags/ActiveMultipliers should be non-nil empty, got %v / %v", s.Tags, s.ActiveMultipliers)
	}
	if s.DiffLine != nil {
		t.Errorf("DiffLine = %v, want nil", *s.DiffLine)
	}
	s2 := mustUnmarshal[ScoredFinding](t, `{"diff_side":"LEFT","confidence":0}`)
	if s2.DiffSide != "LEFT" || s2.Confidence != 0 {
		t.Errorf("override = %+v, want DiffSide=LEFT Confidence=0", s2)
	}
}

func TestGitHubCommentDefaults(t *testing.T) {
	c := mustUnmarshal[GitHubComment](t, "{}")
	if c.Side != "RIGHT" {
		t.Errorf("Side = %q, want RIGHT", c.Side)
	}
	c2 := mustUnmarshal[GitHubComment](t, `{"side":"LEFT"}`)
	if c2.Side != "LEFT" {
		t.Errorf("override Side = %q, want LEFT", c2.Side)
	}
}

func TestAdversaryResultDefaults(t *testing.T) {
	a := mustUnmarshal[AdversaryResult](t, "{}")
	if a.SeverityAdjustment != "none" {
		t.Errorf("SeverityAdjustment = %q, want none", a.SeverityAdjustment)
	}
	if a.HiddenTrap != nil {
		t.Errorf("HiddenTrap = %v, want nil", *a.HiddenTrap)
	}
}

func TestMetaDimensionResultDefaults(t *testing.T) {
	m := mustUnmarshal[MetaDimensionResult](t, "{}")
	if m.Confidence != 0.7 {
		t.Errorf("Confidence = %v, want 0.7", m.Confidence)
	}
}

func TestCoverageGateDefaults(t *testing.T) {
	c := mustUnmarshal[CoverageGate](t, "{}")
	if !c.Confident {
		t.Errorf("Confident = false, want true")
	}
	if c.GapDescriptions == nil || len(c.GapDescriptions) != 0 {
		t.Errorf("GapDescriptions = %v, want empty non-nil", c.GapDescriptions)
	}
	c2 := mustUnmarshal[CoverageGate](t, `{"confident":false}`)
	if c2.Confident {
		t.Errorf("override Confident = true, want false")
	}
}

// TestNestedSliceSeeding verifies each element of a typed slice seeds its own
// defaults on decode (V10 nested clause) — a MetaDimensionResult carrying
// dimensions seeds each ReviewDimension's Priority=1 and default Budget.
func TestNestedSliceSeeding(t *testing.T) {
	m := mustUnmarshal[MetaDimensionResult](t, `{"lens":"semantic","dimensions":[{"id":"x"}]}`)
	if len(m.Dimensions) != 1 {
		t.Fatalf("Dimensions len = %d, want 1", len(m.Dimensions))
	}
	if m.Dimensions[0].Priority != 1 {
		t.Errorf("nested dimension Priority = %d, want 1", m.Dimensions[0].Priority)
	}
	if m.Dimensions[0].Budget != defaultBudgetAllocation() {
		t.Errorf("nested dimension Budget = %+v, want default", m.Dimensions[0].Budget)
	}
}

// TestEmptySlicesMarshalAsArray asserts the seeded list-default fields serialize
// as `[]`, never `null` (design §B.2 empty-list rule) — verified end to end via
// unmarshal("{}") -> marshal.
func TestEmptySlicesMarshalAsArray(t *testing.T) {
	in := mustUnmarshal[ReviewInput](t, "{}")
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	for _, k := range []string{"ignore_paths", "hints"} {
		if string(m[k]) != "[]" {
			t.Errorf("ReviewInput.%s marshaled as %s, want []", k, m[k])
		}
	}
	// Optional (X|None) fields marshal as null, not omitted.
	for _, k := range []string{"pr_url", "max_cost_usd", "max_duration_seconds"} {
		if string(m[k]) != "null" {
			t.Errorf("ReviewInput.%s marshaled as %s, want null", k, m[k])
		}
	}

	f := mustUnmarshal[ReviewFinding](t, "{}")
	fb, _ := json.Marshal(f)
	var fm map[string]json.RawMessage
	_ = json.Unmarshal(fb, &fm)
	if string(fm["tags"]) != "[]" {
		t.Errorf("ReviewFinding.tags marshaled as %s, want []", fm["tags"])
	}
	if string(fm["suggestion"]) != "null" {
		t.Errorf("ReviewFinding.suggestion marshaled as %s, want null", fm["suggestion"])
	}
}

// ---------------------------------------------------------------------------
// V2 (unit half) — marshal -> unmarshal -> marshal is byte-identical for a
// fully-populated ReviewResult, exercising every nested struct, pointer, slice,
// and map field.
// ---------------------------------------------------------------------------

func fullyPopulatedReviewResult() ReviewResult {
	sug := "return early to avoid the nil deref"
	dl := 5
	return ReviewResult{
		ReviewID: "rev_0123456789ab",
		PrURL:    "https://github.com/o/r/pull/7",
		Review: GitHubReview{
			Body:  "## Summary\nLooks risky.",
			Event: "REQUEST_CHANGES",
			Comments: []GitHubComment{
				{Path: "a.go", Line: 12, Side: "RIGHT", Body: "guard this"},
			},
		},
		Findings: []ScoredFinding{
			{
				ID: "f_001", DimensionID: "sec_1", DimensionName: "Security",
				FilePath: "a.go", LineStart: 10, LineEnd: 14, DiffLine: &dl,
				DiffSide: "RIGHT", Severity: "critical", Title: "SQL injection",
				Body: "concatenated query", Suggestion: &sug, Evidence: "line 12",
				Confidence: 0.9, Tags: []string{"security", "correctness"},
				Score: 0.7, ActiveMultipliers: []string{"ai_generated_pr"},
				Blocking: true, BlockingReason: "data-loss risk",
			},
		},
		Summary: ReviewSummary{
			TotalFindings: 1, BySeverity: map[string]int{"critical": 1},
			BlockingCount: 1, AdvisoryCount: 0, DimensionsRun: 3,
			CrossRefInteractions: 0, AdversaryChallenged: 1, AdversaryConfirmed: 1,
			CoverageIterations: 1, AIGeneratedConfidence: 0.3, CostUSD: 0.0,
			DurationSeconds: 42.5, BudgetExhausted: false,
		},
		Metadata: ReviewMetadata{
			Intake:           map[string]any{"pr_type": "feature"},
			Anatomy:          map[string]any{"total_files": float64(2)},
			Plan:             map[string]any{},
			Budget:           map[string]any{},
			Request:          map[string]any{"publisher_job_id": "job_7"},
			AgentInvocations: 5,
			PhasesCompleted:  []string{"intake", "anatomy", "output"},
		},
	}
}

func TestReviewResultRoundTripIdentical(t *testing.T) {
	rr := fullyPopulatedReviewResult()
	b1, err := json.Marshal(rr)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	var rr2 ReviewResult
	if err := json.Unmarshal(b1, &rr2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := json.Marshal(rr2)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("not byte-identical:\n first: %s\nsecond: %s", b1, b2)
	}
	// Top-level key set is exactly ReviewResult.model_dump()'s (design §B.2).
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b2, &m)
	want := []string{"review_id", "pr_url", "review", "findings", "summary", "metadata"}
	if len(m) != len(want) {
		t.Errorf("top-level key count = %d (%v), want %d", len(m), m, len(want))
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
}

func TestReviewInputRoundTripIdentical(t *testing.T) {
	cost := 3.5
	dur := 900
	conc := 4
	cov := 2
	ppr := 42
	pr := "https://github.com/o/r/pull/1"
	in := ReviewInput{
		PrURL: &pr, DiffText: nil, RepoPath: nil, BaseRef: nil, HeadRef: nil,
		Depth: "deep", MaxCostUSD: &cost, MaxDurationSeconds: &dur,
		Focus: "security", IgnorePaths: []string{"*.md"}, Hints: []string{"be strict"},
		Models:                 map[string]string{"reviewer": "anthropic/claude"},
		MaxConcurrentReviewers: &conc, MaxCoverageIterations: &cov, MaxReviewDepth: 3,
		OutputFormat: "github", DryRun: true, PostPRNumber: &ppr, SuggestionMode: "code",
		PublisherJobID: "job_7",
		PullRequest: &PullRequestIdentity{
			Owner: "o", Repository: "r", Number: 7, Title: "Title", URL: "https://github.com/o/r/pull/7",
		},
	}
	b1, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	var in2 ReviewInput
	if err := json.Unmarshal(b1, &in2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, _ := json.Marshal(in2)
	if !bytes.Equal(b1, b2) {
		t.Errorf("ReviewInput not byte-identical:\n first: %s\nsecond: %s", b1, b2)
	}
}
