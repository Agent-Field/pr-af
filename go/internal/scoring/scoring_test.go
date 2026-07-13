package scoring

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func strptr(s string) *string { return &s }

// finding is a small constructor for a ReviewFinding with the fields scoring
// cares about; the rest are pass-through and default-irrelevant to the math.
func finding(title string, sev schemas.Severity, conf float64) schemas.ReviewFinding {
	return schemas.ReviewFinding{
		Title:      title,
		Severity:   sev,
		Confidence: conf,
		Tags:       []string{},
	}
}

func findByTitle(scored []schemas.ScoredFinding, title string) (schemas.ScoredFinding, bool) {
	for _, s := range scored {
		if s.Title == title {
			return s, true
		}
	}
	return schemas.ScoredFinding{}, false
}

// ---------------------------------------------------------------------------
// pyRound — parity trap (design risk #3): Python round() is round-half-to-even
// on the true binary value; Go math.Round is half-away-from-zero.
// ---------------------------------------------------------------------------

func TestPyRound(t *testing.T) {
	cases := []struct {
		in     float64
		digits int
		want   float64
	}{
		// The two required verification cases (exactly representable halves).
		{0.0625, 3, 0.062}, // ties-to-even rounds DOWN (math.Round would give 0.063)
		{0.1875, 3, 0.188}, // ties-to-even rounds UP to the even digit
		// True-binary-value subtlety: these decimals are not exactly
		// representable, so the tie is broken by the real stored value.
		{0.2625, 3, 0.263}, // stored value sits just above the half
		{0.3125, 3, 0.312}, // 5/16 exactly representable -> ties-to-even down
		// The missed_trap score.
		{0.7 * 0.7, 3, 0.49},
		{0.49, 3, 0.49},
	}
	for _, c := range cases {
		if got := pyRound(c.in, c.digits); got != c.want {
			t.Errorf("pyRound(%v, %d) = %v, want %v", c.in, c.digits, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// V3 — the DIVERGENT scoring severity map, tested independently of the
// canonical schemas map. Includes the deliberate disagreements.
// ---------------------------------------------------------------------------

func TestNormSevDivergentMap(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// identity
		{"critical", "critical"},
		{"important", "important"},
		{"suggestion", "suggestion"},
		{"nitpick", "nitpick"},
		// critical tier
		{"high", "critical"},
		{"blocker", "critical"},
		// important tier (DIVERGENT: medium -> important)
		{"medium", "important"},
		{"major", "important"},
		// suggestion tier (DIVERGENT: low -> suggestion)
		{"minor", "suggestion"},
		{"low", "suggestion"},
		// nitpick tier (includes "trivia", which the canonical map lacks)
		{"info", "nitpick"},
		{"trivia", "nitpick"},
		{"trivial", "nitpick"},
		// case / whitespace tolerance
		{"  HIGH ", "critical"},
		{"Medium", "important"},
		{"\tLOW\n", "suggestion"},
		// unknown / empty -> default
		{"", "suggestion"},
		{"   ", "suggestion"},
		{"bogus", "suggestion"},
	}
	for _, c := range cases {
		if got := normSev(c.in); got != c.want {
			t.Errorf("normSev(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The scoring map MUST disagree with the canonical schemas map on exactly these
// inputs — proving they are not accidentally sharing one table.
func TestNormSevDivergesFromCanonical(t *testing.T) {
	divergent := []struct {
		in            string
		scoringWant   string
		canonicalWant schemas.Severity
	}{
		{"medium", "important", "suggestion"},
		{"low", "suggestion", "nitpick"},
	}
	for _, d := range divergent {
		got := normSev(d.in)
		canon := schemas.NormalizeSeverity(d.in, schemas.DefaultSeverity)
		if got != d.scoringWant {
			t.Errorf("normSev(%q) = %q, want %q", d.in, got, d.scoringWant)
		}
		if canon != d.canonicalWant {
			t.Errorf("canonical NormalizeSeverity(%q) = %q, want %q", d.in, canon, d.canonicalWant)
		}
		if string(canon) == got {
			t.Errorf("scoring and canonical maps agree on %q (%q) — they must diverge", d.in, got)
		}
	}
	// "trivia" is recognized by scoring but unknown to the canonical map (which
	// falls back to its default).
	if normSev("trivia") != "nitpick" {
		t.Errorf("normSev(trivia) = %q, want nitpick", normSev("trivia"))
	}
	if got := schemas.NormalizeSeverity("trivia", schemas.DefaultSeverity); got != schemas.DefaultSeverity {
		t.Errorf("canonical NormalizeSeverity(trivia) = %q, want default %q", got, schemas.DefaultSeverity)
	}
}

// ---------------------------------------------------------------------------
// V6 — event mapping. blocking is the source of truth; severity does NOT gate.
// ---------------------------------------------------------------------------

func TestDetermineReviewEvent(t *testing.T) {
	if got := DetermineReviewEvent(nil); got != "APPROVE" {
		t.Errorf("empty -> %q, want APPROVE", got)
	}
	if got := DetermineReviewEvent([]schemas.ScoredFinding{}); got != "APPROVE" {
		t.Errorf("empty slice -> %q, want APPROVE", got)
	}

	// Advisory-only: findings present, none blocking -> COMMENT. This includes a
	// critical-SEVERITY finding that is not marked blocking — severity alone must
	// NOT request changes.
	advisory := []schemas.ScoredFinding{
		{Title: "crit but not blocking", Severity: "critical", Blocking: false},
		{Title: "nit", Severity: "nitpick", Blocking: false},
	}
	if got := DetermineReviewEvent(advisory); got != "COMMENT" {
		t.Errorf("advisory-only (incl. non-blocking critical) -> %q, want COMMENT", got)
	}

	// Any blocking finding -> REQUEST_CHANGES, even if it is a low severity.
	blocking := []schemas.ScoredFinding{
		{Title: "nit", Severity: "nitpick", Blocking: false},
		{Title: "blocking suggestion", Severity: "suggestion", Blocking: true},
	}
	if got := DetermineReviewEvent(blocking); got != "REQUEST_CHANGES" {
		t.Errorf("has blocking -> %q, want REQUEST_CHANGES", got)
	}
}

// ---------------------------------------------------------------------------
// Scoring math: base weights, all four multipliers, and their boundaries.
// Expected scores are computed from scoring.py semantics (Python round()).
// ---------------------------------------------------------------------------

func TestScoreFindingsMultipliers(t *testing.T) {
	cfg := config.DefaultScoringConfig()

	// Call 1: no global multipliers; adversary verdicts drive per-finding mults.
	adv := []schemas.AdversaryResult{
		{FindingTitle: "B", Verdict: "confirmed"},
		{FindingTitle: "C", Verdict: "challenged"},
	}
	findings := []schemas.ReviewFinding{
		finding("A", "critical", 0.9),   // 1.0*0.9 = 0.9
		finding("B", "important", 0.5),  // 0.7*0.5*1.3 = 0.455
		finding("C", "suggestion", 0.6), // 0.3*0.6*0.5 = 0.09
	}
	scored := ScoreFindings(findings, adv, cfg, 0.0, 0)
	if len(scored) != 3 {
		t.Fatalf("call1: got %d findings, want 3", len(scored))
	}
	assertScore(t, scored, "A", 0.9, nil)
	assertScore(t, scored, "B", 0.455, []string{"adversary_confirmed"})
	assertScore(t, scored, "C", 0.09, []string{"adversary_challenged"})

	// Call 2: AI-generated multiplier only (ai_generated > 0.5).
	scored = ScoreFindings([]schemas.ReviewFinding{finding("D", "nitpick", 0.5)}, nil, cfg, 0.8, 0)
	assertScore(t, scored, "D", 0.06, []string{"ai_generated_pr"}) // 0.1*0.5*1.2

	// Call 3: blast-radius multiplier only (blast_radius_size > 10).
	scored = ScoreFindings([]schemas.ReviewFinding{finding("E", "critical", 0.5)}, nil, cfg, 0.0, 20)
	assertScore(t, scored, "E", 0.6, []string{"blast_radius_high"}) // 1.0*0.5*1.2

	// Call 4: all three (adversary_confirmed, ai, blast) stack; check mult order.
	scored = ScoreFindings(
		[]schemas.ReviewFinding{finding("F", "critical", 0.7)},
		[]schemas.AdversaryResult{{FindingTitle: "F", Verdict: "confirmed"}},
		cfg, 0.9, 15,
	)
	// 1.0*0.7*1.3*1.2*1.2 = 1.3104 -> round 1.31
	assertScore(t, scored, "F", 1.31, []string{"adversary_confirmed", "ai_generated_pr", "blast_radius_high"})

	// Call 5: strict boundaries. ai == 0.5 and blast == 10 add NO multiplier.
	scored = ScoreFindings([]schemas.ReviewFinding{finding("BND", "important", 0.5)}, nil, cfg, 0.5, 10)
	assertScore(t, scored, "BND", 0.35, nil) // 0.7*0.5, no multipliers
}

func assertScore(t *testing.T, scored []schemas.ScoredFinding, title string, wantScore float64, wantMults []string) {
	t.Helper()
	f, ok := findByTitle(scored, title)
	if !ok {
		t.Errorf("finding %q not present in scored output", title)
		return
	}
	if f.Score != wantScore {
		t.Errorf("finding %q score = %v, want %v", title, f.Score, wantScore)
	}
	want := wantMults
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(f.ActiveMultipliers, want) {
		t.Errorf("finding %q active_multipliers = %v, want %v", title, f.ActiveMultipliers, want)
	}
	// active_multipliers must always be a non-nil slice (Python: `= []`).
	if f.ActiveMultipliers == nil {
		t.Errorf("finding %q active_multipliers is nil, want non-nil (serializes [])", title)
	}
}

// ---------------------------------------------------------------------------
// Confidence-threshold filtering. Drop is strict `<`; equal-to-threshold keeps.
// ---------------------------------------------------------------------------

func TestScoreFindingsConfidenceThreshold(t *testing.T) {
	cfg := config.DefaultScoringConfig()
	findings := []schemas.ReviewFinding{
		finding("G", "important", 0.25),  // < 0.3  -> drop
		finding("H", "suggestion", 0.35), // < 0.4  -> drop
		finding("I", "nitpick", 0.39),    // < 0.4  -> drop
		finding("J", "critical", 0.19),   // < 0.2  -> drop
		finding("K", "nitpick", 0.4),     // == 0.4 -> KEEP (not strictly less)
	}
	scored := ScoreFindings(findings, nil, cfg, 0.0, 0)
	if len(scored) != 1 {
		t.Fatalf("got %d kept findings, want 1 (only the boundary keep)", len(scored))
	}
	assertScore(t, scored, "K", 0.04, nil) // 0.1*0.4
	if scored[0].ID != "f_000" {
		t.Errorf("kept finding id = %q, want f_000", scored[0].ID)
	}
}

// The DIVERGENT map must be applied INSIDE scoring: a raw "medium"/"low" changes
// both the normalized severity and the base weight relative to the canonical map.
func TestScoreFindingsDivergentSeverityAffectsWeight(t *testing.T) {
	cfg := config.DefaultScoringConfig()

	// medium -> important (base 0.7). Canonical would be suggestion (base 0.3).
	scored := ScoreFindings([]schemas.ReviewFinding{finding("MED", "medium", 0.5)}, nil, cfg, 0.0, 0)
	f, ok := findByTitle(scored, "MED")
	if !ok {
		t.Fatalf("MED dropped unexpectedly")
	}
	if f.Severity != "important" {
		t.Errorf("MED severity = %q, want important (divergent medium mapping)", f.Severity)
	}
	if f.Score != 0.35 { // 0.7*0.5
		t.Errorf("MED score = %v, want 0.35 (base 0.7)", f.Score)
	}

	// low -> suggestion (base 0.3, threshold 0.4). Canonical would be nitpick.
	scored = ScoreFindings([]schemas.ReviewFinding{finding("LOW", "low", 0.5)}, nil, cfg, 0.0, 0)
	f, ok = findByTitle(scored, "LOW")
	if !ok {
		t.Fatalf("LOW dropped unexpectedly")
	}
	if f.Severity != "suggestion" {
		t.Errorf("LOW severity = %q, want suggestion (divergent low mapping)", f.Severity)
	}
	if f.Score != 0.15 { // 0.3*0.5
		t.Errorf("LOW score = %v, want 0.15 (base 0.3)", f.Score)
	}
}

// Pass-through fields land verbatim on the ScoredFinding, with the pydantic
// defaults (diff_side="RIGHT", diff_line=nil, blocking=false) applied.
func TestScoreFindingsPassthroughFields(t *testing.T) {
	cfg := config.DefaultScoringConfig()
	f := schemas.ReviewFinding{
		DimensionID:   "dim-1",
		DimensionName: "Security",
		FilePath:      "src/app.go",
		LineStart:     10,
		LineEnd:       12,
		Severity:      "critical",
		Title:         "SQL injection",
		Body:          "unsanitized input",
		Suggestion:    strptr("use params"),
		Evidence:      "line 10 concat",
		Confidence:    0.9,
		Tags:          []string{"security", "correctness"},
	}
	scored := ScoreFindings([]schemas.ReviewFinding{f}, nil, cfg, 0.0, 0)
	if len(scored) != 1 {
		t.Fatalf("got %d, want 1", len(scored))
	}
	s := scored[0]
	if s.ID != "f_000" || s.DimensionID != "dim-1" || s.DimensionName != "Security" ||
		s.FilePath != "src/app.go" || s.LineStart != 10 || s.LineEnd != 12 ||
		s.Title != "SQL injection" || s.Body != "unsanitized input" ||
		s.Evidence != "line 10 concat" || s.Confidence != 0.9 {
		t.Errorf("pass-through mismatch: %+v", s)
	}
	if s.Suggestion == nil || *s.Suggestion != "use params" {
		t.Errorf("suggestion = %v, want ptr(use params)", s.Suggestion)
	}
	if !reflect.DeepEqual(s.Tags, []string{"security", "correctness"}) {
		t.Errorf("tags = %v, want [security correctness]", s.Tags)
	}
	if s.DiffSide != "RIGHT" {
		t.Errorf("diff_side = %q, want RIGHT (pydantic default)", s.DiffSide)
	}
	if s.DiffLine != nil {
		t.Errorf("diff_line = %v, want nil", s.DiffLine)
	}
	if s.Blocking || s.BlockingReason != "" {
		t.Errorf("blocking=%v reason=%q, want false/empty", s.Blocking, s.BlockingReason)
	}
	if s.Severity != "critical" {
		t.Errorf("severity = %q, want critical", s.Severity)
	}
}

// ---------------------------------------------------------------------------
// missed_trap synthesis (design §D recipe).
// ---------------------------------------------------------------------------

func TestMissedTrapSynthesis(t *testing.T) {
	cfg := config.DefaultScoringConfig()
	adv := []schemas.AdversaryResult{
		{FindingTitle: "X", Verdict: "missed_trap", HiddenTrap: strptr("boom")}, // synthesize
		{FindingTitle: "Y", Verdict: "missed_trap", HiddenTrap: nil},            // skip (nil)
		{FindingTitle: "Z", Verdict: "missed_trap", HiddenTrap: strptr("")},     // skip (empty)
		{FindingTitle: "W", Verdict: "confirmed", HiddenTrap: strptr("nope")},   // skip (not missed_trap)
	}
	scored := ScoreFindings(nil, adv, cfg, 0.0, 0)
	if len(scored) != 1 {
		t.Fatalf("got %d synthesized, want 1", len(scored))
	}
	s := scored[0]
	want := schemas.ScoredFinding{
		ID:                "f_000",
		DimensionID:       "adversary",
		DimensionName:     "Adversary Reviewer",
		FilePath:          "",
		LineStart:         0,
		LineEnd:           0,
		DiffLine:          nil,
		DiffSide:          "RIGHT",
		Severity:          "important",
		Title:             "Hidden trap: X",
		Body:              "boom",
		Suggestion:        nil,
		Evidence:          "",
		Confidence:        0.7,
		Tags:              []string{"hidden-trap", "adversary-found"},
		Score:             0.49,
		ActiveMultipliers: []string{},
		Blocking:          false,
		BlockingReason:    "",
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("missed_trap finding mismatch:\n got  %+v\n want %+v", s, want)
	}
}

// The missed_trap id counter continues past the kept findings, and ids are
// assigned PRE-sort (so a high-scoring trap can end up first with a later id).
func TestMissedTrapIDContinuesCounter(t *testing.T) {
	cfg := config.DefaultScoringConfig()
	findings := []schemas.ReviewFinding{finding("K", "nitpick", 0.5)} // score 0.05, id f_000
	adv := []schemas.AdversaryResult{{FindingTitle: "X", Verdict: "missed_trap", HiddenTrap: strptr("boom")}}
	scored := ScoreFindings(findings, adv, cfg, 0.0, 0)
	if len(scored) != 2 {
		t.Fatalf("got %d, want 2", len(scored))
	}
	k, _ := findByTitle(scored, "K")
	trap, _ := findByTitle(scored, "Hidden trap: X")
	if k.ID != "f_000" {
		t.Errorf("kept finding id = %q, want f_000", k.ID)
	}
	if trap.ID != "f_001" {
		t.Errorf("trap id = %q, want f_001 (counter continues)", trap.ID)
	}
	// trap (0.49) outscores K (0.05) so it sorts first despite the later id.
	if scored[0].ID != "f_001" || scored[1].ID != "f_000" {
		t.Errorf("sorted order ids = [%q %q], want [f_001 f_000]", scored[0].ID, scored[1].ID)
	}
}

// ---------------------------------------------------------------------------
// ID assignment order + stable descending sort with tie preservation.
// ---------------------------------------------------------------------------

func TestScoreFindingsIDOrderAndStableSort(t *testing.T) {
	cfg := config.DefaultScoringConfig()
	findings := []schemas.ReviewFinding{
		finding("first", "important", 0.5),  // score 0.35, id f_000
		finding("top", "critical", 0.9),     // score 0.9,  id f_001
		finding("second", "important", 0.5), // score 0.35, id f_002 (ties with first)
	}
	scored := ScoreFindings(findings, nil, cfg, 0.0, 0)
	if len(scored) != 3 {
		t.Fatalf("got %d, want 3", len(scored))
	}

	// IDs are assigned in INPUT order, before sorting.
	byTitle := map[string]string{}
	for _, s := range scored {
		byTitle[s.Title] = s.ID
	}
	if byTitle["first"] != "f_000" || byTitle["top"] != "f_001" || byTitle["second"] != "f_002" {
		t.Errorf("id assignment = %v, want first=f_000 top=f_001 second=f_002", byTitle)
	}

	// Final order: descending by score; the 0.35 tie keeps input order
	// (first before second) thanks to the stable sort.
	gotOrder := []string{scored[0].Title, scored[1].Title, scored[2].Title}
	wantOrder := []string{"top", "first", "second"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("sorted order = %v, want %v", gotOrder, wantOrder)
	}
}

// ---------------------------------------------------------------------------
// deduplicate_exact — key is (file_path, line_start, line_end, severity).
// ---------------------------------------------------------------------------

func TestDeduplicateExact(t *testing.T) {
	f := func(fp string, ls, le int, sev schemas.Severity, title string) schemas.ReviewFinding {
		return schemas.ReviewFinding{FilePath: fp, LineStart: ls, LineEnd: le, Severity: sev, Title: title}
	}
	findings := []schemas.ReviewFinding{
		f("a.go", 1, 2, "critical", "first"), // keep
		f("a.go", 1, 2, "critical", "dup"),   // drop (exact key match)
		f("a.go", 1, 2, "important", "sev"),  // keep (severity differs)
		f("a.go", 3, 4, "critical", "line"),  // keep (line differs)
		f("b.go", 1, 2, "critical", "file"),  // keep (file differs)
		f("a.go", 1, 2, "critical", "dup2"),  // drop (exact key match again)
	}
	deduped := DeduplicateExact(findings)
	gotTitles := make([]string, len(deduped))
	for i, d := range deduped {
		gotTitles[i] = d.Title
	}
	want := []string{"first", "sev", "line", "file"} // first occurrences, input order
	if !reflect.DeepEqual(gotTitles, want) {
		t.Errorf("deduped titles = %v, want %v", gotTitles, want)
	}
}

// ---------------------------------------------------------------------------
// Empty inputs return non-nil empty slices so JSON marshals to [] (never null),
// matching Python's `list[...] = []`.
// ---------------------------------------------------------------------------

func TestEmptyInputsMarshalAsEmptyArray(t *testing.T) {
	cfg := config.DefaultScoringConfig()

	scored := ScoreFindings(nil, nil, cfg, 0.0, 0)
	if scored == nil {
		t.Fatal("ScoreFindings(nil,...) returned nil, want non-nil empty slice")
	}
	if b, _ := json.Marshal(scored); string(b) != "[]" {
		t.Errorf("ScoreFindings empty marshals to %s, want []", b)
	}

	deduped := DeduplicateExact(nil)
	if deduped == nil {
		t.Fatal("DeduplicateExact(nil) returned nil, want non-nil empty slice")
	}
	if b, _ := json.Marshal(deduped); string(b) != "[]" {
		t.Errorf("DeduplicateExact empty marshals to %s, want []", b)
	}
}
