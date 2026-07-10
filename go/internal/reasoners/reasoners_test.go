package reasoners

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// --- seams -------------------------------------------------------------------

// mockHarness scripts one harness response: on ok, the JSON payload is decoded
// into dest and returned as Parsed (what the SDK does after schema validation);
// on parseFail, a Result with Parsed==nil comes back — the path harnessx.Run
// turns into a seeded default.
type mockHarness struct {
	payload   string
	parseFail bool
	calls     int
	gotPrompt string
	gotOpts   harness.Options
}

func (m *mockHarness) Harness(_ context.Context, prompt string, _ map[string]any, dest any, opts harness.Options) (*harness.Result, error) {
	m.calls++
	m.gotPrompt = prompt
	m.gotOpts = opts
	if m.parseFail {
		return &harness.Result{IsError: true, ErrorMessage: "schema validation failed"}, nil
	}
	if err := json.Unmarshal([]byte(m.payload), dest); err != nil {
		return nil, err
	}
	return &harness.Result{Parsed: dest, Result: m.payload}, nil
}

// fakeAI scripts one structured .ai() response and records the prompt/system.
type fakeAI struct {
	text      string
	gotPrompt string
	gotSystem string
	calls     int
}

func (f *fakeAI) AI(_ context.Context, prompt string, opts ...ai.Option) (*ai.Response, error) {
	f.calls++
	f.gotPrompt = prompt
	var req ai.Request
	for _, o := range opts {
		if err := o(&req); err != nil {
			return nil, err
		}
	}
	for _, msg := range req.Messages {
		if msg.Role == "system" && len(msg.Content) > 0 {
			f.gotSystem = msg.Content[0].Text
		}
	}
	return &ai.Response{
		Choices: []ai.Choice{{
			Message: ai.Message{
				Role:    "assistant",
				Content: []ai.ContentPart{{Type: "text", Text: f.text}},
			},
		}},
	}, nil
}

func keySet(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wantKeys(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keySet(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("key set mismatch:\n got  %v\n want %v", got, want)
	}
}

// fixturePR mirrors the Python fixture used to capture helper goldens
// (helpers_test.go documents the capture run).
func fixturePR() schemas.GitHubPRData {
	paths := []string{
		"src/auth/login.py", "db/migrations/0001_init.sql", "web/App.tsx",
		"cmd/main.go", "config/settings.yml", "tests/test_login.py",
		"Dockerfile", "README.md", "script.unknownext",
	}
	files := make([]schemas.ChangedFile, len(paths))
	for i, p := range paths {
		files[i] = schemas.ChangedFile{Path: p, Status: "modified", Additions: 3, Deletions: 1}
	}
	return schemas.GitHubPRData{
		Owner: "o", Repo: "r", Number: 7,
		Title:       "Add OAuth login",
		Description: "  Implements OAuth2 login flow.  ",
		Labels:      []string{"auth"},
		Author:      "alice",
		BaseSHA:     "b", HeadSHA: "h",
		CommitMessages: []string{"init", "Co-Authored-By: Claude <x>", "fix tests"},
		ChangedFiles:   files,
	}
}

var intakeKeys = []string{
	"pr_type", "complexity", "languages", "areas_touched",
	"risk_signals", "ai_generated", "review_depth", "pr_summary",
}

// --- intake_phase -------------------------------------------------------------

// Contract: a confident gate yields the full IntakeResult key set with the
// deterministic extractor values; depth "auto" resolves through _auto_depth.
func TestIntakePhaseConfidentGate(t *testing.T) {
	aiSeam := &fakeAI{text: `{"pr_type":"feature","complexity":"trivial","confident":true}`}
	h := &mockHarness{}
	out, err := IntakePhase(context.Background(), Deps{Harness: h, AI: aiSeam}, IntakeInput{
		PRData: fixturePR(), Depth: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, intakeKeys...)
	if h.calls != 0 {
		t.Fatalf("confident gate must not invoke the harness fallback, got %d calls", h.calls)
	}
	if aiSeam.gotSystem != prompts.IntakeGateSystem {
		t.Fatalf("system prompt = %q", aiSeam.gotSystem)
	}
	if out["pr_type"] != "feature" || out["complexity"] != "trivial" {
		t.Fatalf("gate fields not propagated: %v", out)
	}
	if out["review_depth"] != "quick" { // auto + trivial -> quick (Python golden)
		t.Fatalf("review_depth = %v, want quick", out["review_depth"])
	}
	if out["ai_generated"] != 0.2 { // Python golden for the fixture
		t.Fatalf("ai_generated = %v, want 0.2", out["ai_generated"])
	}
	if out["pr_summary"] != "Implements OAuth2 login flow." {
		t.Fatalf("pr_summary = %v", out["pr_summary"])
	}
	wantLangs := []any{"go", "markdown", "python", "sql", "typescript", "yaml"}
	if !reflect.DeepEqual(out["languages"], wantLangs) {
		t.Fatalf("languages = %v, want %v", out["languages"], wantLangs)
	}
	wantAreas := []any{"auth", "database", "frontend", "tests", "config", "infra"}
	if !reflect.DeepEqual(out["areas_touched"], wantAreas) {
		t.Fatalf("areas_touched = %v, want %v", out["areas_touched"], wantAreas)
	}
	wantRisk := []any{
		"touches authentication or security-sensitive paths",
		"modifies data model or schema-affecting code",
		"includes configuration changes",
		"test behavior updated",
	}
	if !reflect.DeepEqual(out["risk_signals"], wantRisk) {
		t.Fatalf("risk_signals = %v", out["risk_signals"])
	}
}

// Contract: an unconfident gate escalates to the harness; a parsed fallback
// result dumps the full key set.
func TestIntakePhaseFallbackParsed(t *testing.T) {
	aiSeam := &fakeAI{text: `{"pr_type":"","complexity":"","confident":false}`}
	h := &mockHarness{payload: `{
		"pr_type":"refactor","complexity":"standard","languages":["go"],
		"areas_touched":["api"],"risk_signals":[],"ai_generated":0.1,
		"review_depth":"standard","pr_summary":"s"}`}
	out, err := IntakePhase(context.Background(), Deps{Harness: h, AI: aiSeam}, IntakeInput{
		PRData: fixturePR(), Depth: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("harness calls = %d, want 1", h.calls)
	}
	if !strings.HasPrefix(h.gotPrompt, "Classify this pull request for a multi-agent review pipeline.") {
		t.Fatalf("fallback prompt mismatch: %q", h.gotPrompt[:60])
	}
	wantKeys(t, out, intakeKeys...)
	if out["pr_type"] != "refactor" {
		t.Fatalf("pr_type = %v", out["pr_type"])
	}
}

// Contract: fallback parse failure returns Python's literal empty dict.
func TestIntakePhaseFallbackParseFailReturnsEmpty(t *testing.T) {
	aiSeam := &fakeAI{text: `{"pr_type":"","complexity":"","confident":false}`}
	h := &mockHarness{parseFail: true}
	out, err := IntakePhase(context.Background(), Deps{Harness: h, AI: aiSeam}, IntakeInput{
		PRData: fixturePR(), Depth: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("want {}, got %v", out)
	}
}

// --- anatomy_phase -------------------------------------------------------------

var anatomyKeys = []string{
	"files", "clusters", "blast_radius", "dependency_graph", "stats",
	"pr_narrative", "risk_surfaces", "unrelated_changes", "intent_gaps", "context_notes",
}

// Contract: anatomy merges deterministic diff decomposition with the parsed
// semantic fields; empty diff falls back to metadata-derived FileChanges.
func TestAnatomyPhaseHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{
		"pr_narrative":"replaces X with Y","risk_surfaces":["callers of X"],
		"unrelated_changes":[],"intent_gaps":["undocumented flag"],"context_notes":"n"}`}
	out, err := AnatomyPhase(context.Background(), Deps{Harness: h}, AnatomyInput{
		PRData: fixturePR(), Intake: schemas.IntakeResult{PrType: "feature", Complexity: "standard", PrSummary: "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, anatomyKeys...)
	if out["pr_narrative"] != "replaces X with Y" {
		t.Fatalf("pr_narrative = %v", out["pr_narrative"])
	}
	files := out["files"].([]any)
	if len(files) != 9 { // metadata fallback: one FileChange per changed file
		t.Fatalf("files len = %d, want 9", len(files))
	}
	if dg := out["dependency_graph"].(map[string]any); len(dg) != 0 {
		t.Fatalf("dependency_graph = %v, want {}", dg)
	}
	if !strings.HasPrefix(h.gotPrompt, "You are a senior engineer performing structural analysis") {
		t.Fatalf("prompt mismatch: %q", h.gotPrompt[:50])
	}
}

// Contract: semantic parse failure degrades to empty narrative fields with the
// full key set intact (never an error).
func TestAnatomyPhaseParseFailSeedsEmptySemantics(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := AnatomyPhase(context.Background(), Deps{Harness: h}, AnatomyInput{
		PRData: fixturePR(), Intake: schemas.IntakeResult{},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, anatomyKeys...)
	if out["pr_narrative"] != "" || out["context_notes"] != "" {
		t.Fatalf("want empty semantics, got %v / %v", out["pr_narrative"], out["context_notes"])
	}
	if rs := out["risk_surfaces"].([]any); len(rs) != 0 {
		t.Fatalf("risk_surfaces = %v, want []", rs)
	}
}

// --- planning_phase -------------------------------------------------------------

// Contract: a parsed plan dumps the four ReviewPlan keys; an absent
// total_budget lands on the pydantic default_factory seed (0.5/60/3/2).
func TestPlanningPhaseHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{
		"dimensions":[{"id":"d1","name":"N","review_prompt":"p","target_files":["a.go"]}],
		"cross_ref_hints":["h"],"ai_adjusted":true}`}
	out, err := PlanningPhase(context.Background(), Deps{Harness: h}, PlanningInput{Depth: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "dimensions", "cross_ref_hints", "ai_adjusted", "total_budget")
	dims := out["dimensions"].([]any)
	dim := dims[0].(map[string]any)
	if dim["priority"] != float64(1) { // seeded ReviewDimension default
		t.Fatalf("priority = %v, want 1", dim["priority"])
	}
	tb := out["total_budget"].(map[string]any)
	if tb["max_cost_usd"] != 0.5 || tb["max_duration_seconds"] != float64(60) {
		t.Fatalf("total_budget not seeded: %v", tb)
	}
}

// Contract: parse failure returns Python's literal two-key fallback.
func TestPlanningPhaseParseFailFallback(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := PlanningPhase(context.Background(), Deps{Harness: h}, PlanningInput{Depth: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "dimensions", "cross_ref_hints")
	if len(out["dimensions"].([]any)) != 0 || len(out["cross_ref_hints"].([]any)) != 0 {
		t.Fatalf("want empty lists, got %v", out)
	}
}

// --- meta selectors -------------------------------------------------------------

var metaKeys = []string{"lens", "dimensions", "confidence", "rationale"}

// Contract: each selector forces its own lens (even over a model-supplied
// value) and emits the MetaDimensionResult key set.
func TestMetaSelectorsForceLens(t *testing.T) {
	cases := []struct {
		lens string
		fn   func(context.Context, Deps, MetaInput) (map[string]any, error)
	}{
		{"semantic", MetaSemantic},
		{"mechanical", MetaMechanical},
		{"systemic", MetaSystemic},
	}
	for _, tc := range cases {
		t.Run(tc.lens, func(t *testing.T) {
			h := &mockHarness{payload: `{
				"lens":"WRONG",
				"dimensions":[{"id":"x","name":"X","review_prompt":"p","target_files":["a"],"priority":5}],
				"confidence":0.9,"rationale":"r"}`}
			out, err := tc.fn(context.Background(), Deps{Harness: h}, MetaInput{Depth: "standard"})
			if err != nil {
				t.Fatal(err)
			}
			wantKeys(t, out, metaKeys...)
			if out["lens"] != tc.lens {
				t.Fatalf("lens = %v, want %s", out["lens"], tc.lens)
			}
			if out["confidence"] != 0.9 {
				t.Fatalf("confidence = %v", out["confidence"])
			}
			if !strings.Contains(h.gotPrompt, strings.ToUpper(tc.lens)) {
				t.Fatalf("prompt does not carry the %s lens", tc.lens)
			}
		})
	}
}

// Contract: parse failure yields lens + empty dimensions + the seeded 0.7
// confidence (Python's MetaDimensionResult(lens=..., dimensions=[])).
func TestMetaSelectorParseFail(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := MetaMechanical(context.Background(), Deps{Harness: h}, MetaInput{Depth: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, metaKeys...)
	if out["lens"] != "mechanical" {
		t.Fatalf("lens = %v", out["lens"])
	}
	if len(out["dimensions"].([]any)) != 0 {
		t.Fatalf("dimensions = %v, want []", out["dimensions"])
	}
	if out["confidence"] != 0.7 {
		t.Fatalf("confidence = %v, want seeded 0.7", out["confidence"])
	}
	if out["rationale"] != "" {
		t.Fatalf("rationale = %v", out["rationale"])
	}
}

// --- review_dimension -------------------------------------------------------------

var reviewDimKeys = []string{"findings", "sub_reviews", "current_depth"}

var findingDumpKeys = []string{
	"dimension_id", "dimension_name", "file_path", "line_start", "line_end",
	"hunk_context", "severity", "title", "body", "suggestion", "evidence",
	"confidence", "tags",
}

// Contract: findings dump with the full ReviewFinding key set; sub_reviews are
// sliced to 2 THEN filtered on review_prompt+target_files; current_depth echoes
// the input.
func TestReviewDimensionHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{
		"findings":[{"title":"T","severity":"high","file_path":"a.go","line_start":3}],
		"sub_reviews":[
			{"reason":"r1","review_prompt":"p1","target_files":["a.go"]},
			{"reason":"r2","review_prompt":"","target_files":["b.go"]},
			{"reason":"r3","review_prompt":"p3","target_files":["c.go"]}
		]}`}
	out, err := ReviewDimension(context.Background(), Deps{Harness: h}, ReviewDimensionInput{
		ReviewPrompt: "Investigate X", TargetFiles: []string{"a.go"},
		CurrentDepth: 1, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, reviewDimKeys...)
	if out["current_depth"] != 1 {
		t.Fatalf("current_depth = %v", out["current_depth"])
	}
	findings := out["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0].(map[string]any)
	wantKeys(t, f, findingDumpKeys...)
	if f["severity"] != "important" { // "high" coerced by the canonical map
		t.Fatalf("severity = %v, want important", f["severity"])
	}
	if f["confidence"] != 0.5 { // seeded pydantic default
		t.Fatalf("confidence = %v, want 0.5", f["confidence"])
	}
	// [:2] slice happens BEFORE the filter: sr2 (empty prompt) is inside the
	// slice and dropped, sr3 is outside it — only sr1 survives.
	subs := out["sub_reviews"].([]any)
	if len(subs) != 1 {
		t.Fatalf("sub_reviews = %v, want exactly 1", subs)
	}
	sub := subs[0].(map[string]any)
	wantKeys(t, sub, "reason", "review_prompt", "target_files", "context_files", "priority")
	if sub["priority"] != 1 { // seeded default
		t.Fatalf("priority = %v", sub["priority"])
	}
}

// Contract: at max depth no sub-reviews are forwarded even if the model
// returned some.
func TestReviewDimensionAtMaxDepthDropsSubReviews(t *testing.T) {
	h := &mockHarness{payload: `{
		"findings":[],
		"sub_reviews":[{"reason":"r","review_prompt":"p","target_files":["a"]}]}`}
	out, err := ReviewDimension(context.Background(), Deps{Harness: h}, ReviewDimensionInput{
		ReviewPrompt: "x", TargetFiles: []string{"a"}, CurrentDepth: 2, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["sub_reviews"].([]any)) != 0 {
		t.Fatalf("sub_reviews = %v, want []", out["sub_reviews"])
	}
	if !strings.Contains(h.gotPrompt, "You are at maximum review depth.") {
		t.Fatal("prompt should carry the no-spawn instruction")
	}
}

// Contract: schema parse failure reports zero findings (with the key set
// intact), never an error.
func TestReviewDimensionParseFail(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := ReviewDimension(context.Background(), Deps{Harness: h}, ReviewDimensionInput{
		ReviewPrompt: "x", TargetFiles: []string{"a"}, CurrentDepth: 0, MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, reviewDimKeys...)
	if len(out["findings"].([]any)) != 0 || len(out["sub_reviews"].([]any)) != 0 {
		t.Fatalf("want empty findings/sub_reviews, got %v", out)
	}
	if out["current_depth"] != 0 {
		t.Fatalf("current_depth = %v", out["current_depth"])
	}
}

// --- compound finder / dedup -------------------------------------------------------------

// Contract: fewer than two findings short-circuits without a harness call.
func TestCompoundFinderShortCircuit(t *testing.T) {
	h := &mockHarness{}
	out, err := CompoundFinderPhase(context.Background(), Deps{Harness: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "only"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "findings")
	if len(out["findings"].([]any)) != 0 || h.calls != 0 {
		t.Fatalf("want no harness call and [], got calls=%d out=%v", h.calls, out)
	}
}

var compoundFindingKeys = []string{
	"title", "severity", "file_path", "line_start", "line_end", "body",
	"evidence", "suggestion", "confidence", "tags", "contributing_findings",
}

// Contract: compound findings dump the _CompoundFinding key set with seeded
// defaults for absent fields.
func TestCompoundFinderHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{"findings":[{"title":"combo","contributing_findings":["a","b"]}]}`}
	out, err := CompoundFinderPhase(context.Background(), Deps{Harness: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	findings := out["findings"].([]any)
	f := findings[0].(map[string]any)
	wantKeys(t, f, compoundFindingKeys...)
	if f["severity"] != "suggestion" || f["confidence"] != 0.5 {
		t.Fatalf("seeded defaults missing: %v", f)
	}
}

// Contract: compound parse failure degrades to an empty findings list.
func TestCompoundFinderParseFail(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := CompoundFinderPhase(context.Background(), Deps{Harness: h}, CompoundFinderInput{
		ClusterFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out["findings"])
	}
}

// Contract: <=1 compound findings short-circuit with the fixed reasoning
// string; valid indices filter; empty/invalid indices keep everything.
func TestCompoundDedupPhase(t *testing.T) {
	// Short circuit.
	out, err := CompoundDedupPhase(context.Background(), Deps{}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices", "reasoning")
	if out["reasoning"] != "single finding, no dedup needed" {
		t.Fatalf("reasoning = %v", out["reasoning"])
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0}) {
		t.Fatalf("keep_indices = %v", out["keep_indices"])
	}

	// Valid subset (out-of-range dropped).
	h := &mockHarness{payload: `{"keep_indices":[1,7,-1],"reasoning":"r"}`}
	out, err = CompoundDedupPhase(context.Background(), Deps{Harness: h}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{1}) || out["reasoning"] != "r" {
		t.Fatalf("got %v", out)
	}

	// Parse failure -> keep all, reasoning "".
	h = &mockHarness{parseFail: true}
	out, err = CompoundDedupPhase(context.Background(), Deps{Harness: h}, CompoundDedupInput{
		CompoundFindings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}, {Title: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 1, 2}) || out["reasoning"] != "" {
		t.Fatalf("got %v", out)
	}
}

// --- post_worthiness_gate -------------------------------------------------------------

// Contract: <=1 finding returns keep_indices ONLY (no reasoning key — exact
// Python branch); >1 filters valid indices; empty/parse-fail keeps everything.
func TestPostWorthinessGate(t *testing.T) {
	// Zero findings: keep_indices [] and no reasoning key.
	out, err := PostWorthinessGate(context.Background(), Deps{}, PostWorthinessInput{})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices")
	if len(out["keep_indices"].([]int)) != 0 {
		t.Fatalf("keep_indices = %v", out["keep_indices"])
	}

	// One finding: [0], still no reasoning key, no harness call.
	h := &mockHarness{}
	out, err = PostWorthinessGate(context.Background(), Deps{Harness: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "t"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices")
	if !reflect.DeepEqual(out["keep_indices"], []int{0}) || h.calls != 0 {
		t.Fatalf("got %v calls=%d", out, h.calls)
	}

	// Judged subset.
	h = &mockHarness{payload: `{"keep_indices":[0,2,9],"reasoning":"kept real bugs"}`}
	out, err = PostWorthinessGate(context.Background(), Deps{Harness: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}, {Title: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "keep_indices", "reasoning")
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 2}) || out["reasoning"] != "kept real bugs" {
		t.Fatalf("got %v", out)
	}

	// Parse failure never silences everything.
	h = &mockHarness{parseFail: true}
	out, err = PostWorthinessGate(context.Background(), Deps{Harness: h}, PostWorthinessInput{
		Findings: []schemas.ReviewFinding{{Title: "a"}, {Title: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out["keep_indices"], []int{0, 1}) {
		t.Fatalf("got %v", out)
	}
}

// --- evidence_verifier -------------------------------------------------------------

var verifiedFindingKeys = []string{
	"title", "verified", "actual_behavior", "revised_severity",
	"revised_confidence", "verification_notes",
}

// Contract: verified findings dump the _VerifiedFinding key set; absent fields
// land on seeded defaults (verified=true, revised_confidence=0.5).
func TestEvidenceVerifierHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{"verified_findings":[{"title":"T","verified":false,"revised_severity":"nitpick"}]}`}
	out, err := EvidenceVerifier(context.Background(), Deps{Harness: h}, EvidenceVerifierInput{
		Findings: []schemas.ReviewFinding{{Title: "T", Severity: "important"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "verified_findings")
	vf := out["verified_findings"].([]any)[0].(map[string]any)
	wantKeys(t, vf, verifiedFindingKeys...)
	if vf["verified"] != false || vf["revised_confidence"] != 0.5 {
		t.Fatalf("got %v", vf)
	}
}

// Contract: parse failure degrades to an empty verified_findings list.
func TestEvidenceVerifierParseFail(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := EvidenceVerifier(context.Background(), Deps{Harness: h}, EvidenceVerifierInput{
		Findings: []schemas.ReviewFinding{{Title: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["verified_findings"].([]any)) != 0 {
		t.Fatalf("got %v", out)
	}
}

// --- adversary_phase -------------------------------------------------------------

var adversaryResultKeys = []string{
	"finding_title", "verdict", "reason", "severity_adjustment", "hidden_trap",
}

// Contract: results dump the AdversaryResult key set (seeded
// severity_adjustment="none", hidden_trap null); skepticism escalates in the
// prompt above 0.5 AI confidence.
func TestAdversaryPhaseHappyPath(t *testing.T) {
	h := &mockHarness{payload: `{"results":[{"finding_title":"T","verdict":"confirmed","reason":"r"}]}`}
	out, err := AdversaryPhase(context.Background(), Deps{Harness: h}, AdversaryInput{
		Findings:              []schemas.ReviewFinding{{Title: "T"}},
		AIGeneratedConfidence: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "results")
	r := out["results"].([]any)[0].(map[string]any)
	wantKeys(t, r, adversaryResultKeys...)
	if r["severity_adjustment"] != "none" {
		t.Fatalf("severity_adjustment = %v, want seeded none", r["severity_adjustment"])
	}
	if r["hidden_trap"] != nil {
		t.Fatalf("hidden_trap = %v, want null", r["hidden_trap"])
	}
	if !strings.Contains(h.gotPrompt, "Skepticism mode: high") {
		t.Fatal("skepticism should escalate above 0.5")
	}
	if !strings.Contains(h.gotPrompt, "AI-generated confidence: 0.8") {
		t.Fatal("prompt should carry the AI confidence")
	}
}

// Contract: parse failure degrades to an empty results list.
func TestAdversaryPhaseParseFail(t *testing.T) {
	h := &mockHarness{parseFail: true}
	out, err := AdversaryPhase(context.Background(), Deps{Harness: h}, AdversaryInput{
		Findings: []schemas.ReviewFinding{{Title: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["results"].([]any)) != 0 {
		t.Fatalf("got %v", out)
	}
}

// --- deepen_findings -------------------------------------------------------------

var deepenFindingKeys = []string{
	"dimension_id", "dimension_name", "file_path", "line_start", "line_end",
	"severity", "title", "body", "suggestion", "evidence", "confidence", "tags",
}

// Contract: no non-empty patches short-circuits without a harness call; parsed
// findings carry the _DeepenFinding seeds for absent fields.
func TestDeepenFindings(t *testing.T) {
	h := &mockHarness{}
	out, err := DeepenFindings(context.Background(), Deps{Harness: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: ""}}, // filtered to empty
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 || h.calls != 0 {
		t.Fatalf("want short-circuit, got calls=%d out=%v", h.calls, out)
	}

	h = &mockHarness{payload: `{"findings":[{"title":"wrong arg","file_path":"a.go","line_start":3}]}`}
	out, err = DeepenFindings(context.Background(), Deps{Harness: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "findings")
	f := out["findings"].([]any)[0].(map[string]any)
	wantKeys(t, f, deepenFindingKeys...)
	if f["dimension_id"] != "literal-verify" ||
		f["dimension_name"] != "Literal-Correctness Verifier" ||
		f["severity"] != "important" || f["confidence"] != 0.7 {
		t.Fatalf("seeded defaults missing: %v", f)
	}

	h = &mockHarness{parseFail: true}
	out, err = DeepenFindings(context.Background(), Deps{Harness: h}, DeepenInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["findings"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out["findings"])
	}
}

// --- extract_obligations / verify_obligation -------------------------------------------------------------

// Contract: obligations dump {id, where, relies_on, property}; empty patches
// short-circuit; parse failure degrades to an empty list.
func TestExtractObligations(t *testing.T) {
	h := &mockHarness{}
	out, err := ExtractObligations(context.Background(), Deps{Harness: h}, ExtractObligationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["obligations"].([]any)) != 0 || h.calls != 0 {
		t.Fatalf("want short-circuit, got %v", out)
	}

	h = &mockHarness{payload: `{"obligations":[{"id":"o1","where":"w","relies_on":"r","property":"p"}]}`}
	out, err = ExtractObligations(context.Background(), Deps{Harness: h}, ExtractObligationsInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "obligations")
	o := out["obligations"].([]any)[0].(map[string]any)
	wantKeys(t, o, "id", "where", "relies_on", "property")

	h = &mockHarness{parseFail: true}
	out, err = ExtractObligations(context.Background(), Deps{Harness: h}, ExtractObligationsInput{
		DiffPatches: OrderedPatches{{Key: "a.go", Val: "+x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["obligations"].([]any)) != 0 {
		t.Fatalf("want [], got %v", out)
	}
}

var verdictKeys = []string{
	"holds", "title", "severity", "file_path", "line_start", "line_end",
	"body", "evidence", "suggestion", "confidence",
}

// Contract: a parsed verdict dumps all ten keys; parse failure returns the
// seeded holds=true verdict (severity="important", confidence=0.7).
func TestVerifyObligation(t *testing.T) {
	h := &mockHarness{payload: `{
		"holds":false,"title":"key mismatch","severity":"critical","file_path":"a.go",
		"line_start":10,"line_end":12,"body":"b","evidence":"e","suggestion":"s","confidence":0.9}`}
	out, err := VerifyObligation(context.Background(), Deps{Harness: h}, VerifyObligationInput{
		Obligation: map[string]any{"id": "o1", "where": "w", "relies_on": "r", "property": "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, verdictKeys...)
	if out["holds"] != false || out["severity"] != "critical" {
		t.Fatalf("got %v", out)
	}
	if !strings.Contains(h.gotPrompt, "- WHERE (the changed code): w\n") {
		t.Fatalf("obligation fields not in prompt: %q", h.gotPrompt)
	}

	h = &mockHarness{parseFail: true}
	out, err = VerifyObligation(context.Background(), Deps{Harness: h}, VerifyObligationInput{
		Obligation: map[string]any{"where": "w", "relies_on": "r", "property": "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, verdictKeys...)
	if out["holds"] != true || out["severity"] != "important" || out["confidence"] != 0.7 {
		t.Fatalf("seeded verdict wrong: %v", out)
	}
	if out["suggestion"] != nil {
		t.Fatalf("suggestion = %v, want null", out["suggestion"])
	}
}

// --- coverage_gate -------------------------------------------------------------

// Contract: the .ai() gate dumps {fully_covered, gap_descriptions, confident};
// absent keys land on the pydantic seeds (confident=true, gaps=[]).
func TestCoverageGate(t *testing.T) {
	aiSeam := &fakeAI{text: `{"fully_covered":false,"gap_descriptions":["cluster_1 unreviewed"]}`}
	out, err := CoverageGate(context.Background(), Deps{AI: aiSeam}, CoverageGateInput{
		Anatomy: schemas.AnatomyResult{
			Clusters: []schemas.ChangeCluster{{ID: "cluster_0", Name: "root", Files: []string{"a.go"}}},
		},
		ReviewedClusters:       []string{"cluster_0"},
		DimensionNamesReviewed: []string{"Dim A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys(t, out, "fully_covered", "gap_descriptions", "confident")
	if out["fully_covered"] != false || out["confident"] != true {
		t.Fatalf("got %v", out)
	}
	if aiSeam.gotSystem != prompts.CoverageGateSystem {
		t.Fatalf("system = %q", aiSeam.gotSystem)
	}
	if !strings.Contains(aiSeam.gotPrompt, "Dimensions already reviewed: Dim A.") {
		t.Fatalf("prompt = %q", aiSeam.gotPrompt)
	}

	// Empty response object: everything seeded.
	aiSeam = &fakeAI{text: `{}`}
	out, err = CoverageGate(context.Background(), Deps{AI: aiSeam}, CoverageGateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out["confident"] != true || out["fully_covered"] != false {
		t.Fatalf("seeds wrong: %v", out)
	}
	if len(out["gap_descriptions"].([]any)) != 0 {
		t.Fatalf("gap_descriptions = %v", out["gap_descriptions"])
	}
}

// --- error propagation -------------------------------------------------------------

type erroringHarness struct{}

func (erroringHarness) Harness(context.Context, string, map[string]any, any, harness.Options) (*harness.Result, error) {
	return nil, context.DeadlineExceeded
}

// Contract: harness transport errors propagate out of every harness-backed
// reasoner (Python lets the exception escape).
func TestHarnessErrorPropagates(t *testing.T) {
	deps := Deps{Harness: erroringHarness{}}
	if _, err := AnatomyPhase(context.Background(), deps, AnatomyInput{PRData: fixturePR()}); err == nil {
		t.Fatal("anatomy: want error")
	}
	if _, err := ReviewDimension(context.Background(), deps, ReviewDimensionInput{TargetFiles: []string{"a"}}); err == nil {
		t.Fatal("review_dimension: want error")
	}
	if _, err := VerifyObligation(context.Background(), deps, VerifyObligationInput{Obligation: map[string]any{}}); err == nil {
		t.Fatal("verify_obligation: want error")
	}
}
