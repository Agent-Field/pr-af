package reasoners

import (
	"encoding/json"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// This file ports the private harness-result models declared at module level in
// reasoners/harnesses.py (design §C.1 "private harness-result structs"). They
// live here — not in internal/schemas — because nothing outside the reasoners
// consumes them: they are only the typed targets of harnessx.Run.
//
// Every struct with at least one non-zero pydantic default (or a
// default_factory=list field) gets an UnmarshalJSON that seeds the default
// before decoding (the seed-then-`type alias` idiom from schemas/defaults.go).
// Missing or unparseable harness output is rejected before these values are
// consumed. Note _SubReviewRequest is structurally identical to
// schemas.SubReviewRequest (same fields, defaults, and json tags), so the
// committed schemas struct is reused instead of duplicated.

// anatomySemanticResult ports _AnatomySemanticResult.
type anatomySemanticResult struct {
	PrNarrative      string   `json:"pr_narrative"`
	RiskSurfaces     []string `json:"risk_surfaces"`
	UnrelatedChanges []string `json:"unrelated_changes"`
	IntentGaps       []string `json:"intent_gaps"`
	ContextNotes     string   `json:"context_notes"`
}

func (a *anatomySemanticResult) UnmarshalJSON(b []byte) error {
	*a = anatomySemanticResult{
		RiskSurfaces:     []string{},
		UnrelatedChanges: []string{},
		IntentGaps:       []string{},
	}
	type alias anatomySemanticResult
	return json.Unmarshal(b, (*alias)(a))
}

// reviewFindingsResult ports _ReviewFindingsResult. The sub_reviews element
// type reuses schemas.SubReviewRequest (identical to _SubReviewRequest).
type reviewFindingsResult struct {
	Findings        []schemas.ReviewFinding    `json:"findings"`
	SubReviews      []schemas.SubReviewRequest `json:"sub_reviews"`
	findingsPresent bool                       `json:"-"`
}

func (r *reviewFindingsResult) UnmarshalJSON(b []byte) error {
	*r = reviewFindingsResult{
		Findings:   []schemas.ReviewFinding{},
		SubReviews: []schemas.SubReviewRequest{},
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	_, r.findingsPresent = fields["findings"]
	type alias reviewFindingsResult
	return json.Unmarshal(b, (*alias)(r))
}

// hasExplicitFindings distinguishes an intentional clean review from a missing
// or null collection even if schema validation is accidentally bypassed.
func (r reviewFindingsResult) hasExplicitFindings() bool {
	return r.findingsPresent && r.Findings != nil
}

// compoundFinding ports _CompoundFinding.
type compoundFinding struct {
	Title                string           `json:"title"`
	Severity             schemas.Severity `json:"severity"`
	FilePath             string           `json:"file_path"`
	LineStart            int              `json:"line_start"`
	LineEnd              int              `json:"line_end"`
	Body                 string           `json:"body"`
	Evidence             string           `json:"evidence"`
	Suggestion           *string          `json:"suggestion"`
	Confidence           float64          `json:"confidence"`
	Tags                 []string         `json:"tags"`
	ContributingFindings []string         `json:"contributing_findings"`
}

func (c *compoundFinding) UnmarshalJSON(b []byte) error {
	*c = compoundFinding{
		Severity:             schemas.DefaultSeverity,
		Confidence:           0.5,
		Tags:                 []string{},
		ContributingFindings: []string{},
	}
	type alias compoundFinding
	return json.Unmarshal(b, (*alias)(c))
}

// compoundResult ports _CompoundResult.
type compoundResult struct {
	Findings []compoundFinding `json:"findings"`
}

func (c *compoundResult) UnmarshalJSON(b []byte) error {
	*c = compoundResult{Findings: []compoundFinding{}}
	type alias compoundResult
	return json.Unmarshal(b, (*alias)(c))
}

// postWorthinessResult ports _PostWorthinessResult.
type postWorthinessResult struct {
	KeepIndices []int  `json:"keep_indices"`
	Reasoning   string `json:"reasoning"`
}

func (p *postWorthinessResult) UnmarshalJSON(b []byte) error {
	*p = postWorthinessResult{KeepIndices: []int{}}
	type alias postWorthinessResult
	return json.Unmarshal(b, (*alias)(p))
}

// compoundDedupResult ports _CompoundDedupResult.
type compoundDedupResult struct {
	KeepIndices []int  `json:"keep_indices"`
	Reasoning   string `json:"reasoning"`
}

func (c *compoundDedupResult) UnmarshalJSON(b []byte) error {
	*c = compoundDedupResult{KeepIndices: []int{}}
	type alias compoundDedupResult
	return json.Unmarshal(b, (*alias)(c))
}

// adversaryPhaseResult ports _AdversaryPhaseResult.
type adversaryPhaseResult struct {
	Results []schemas.AdversaryResult `json:"results"`
}

func (a *adversaryPhaseResult) UnmarshalJSON(b []byte) error {
	*a = adversaryPhaseResult{Results: []schemas.AdversaryResult{}}
	type alias adversaryPhaseResult
	return json.Unmarshal(b, (*alias)(a))
}

// verifiedFinding ports _VerifiedFinding. revised_severity is a plain string in
// Python (str = ""), NOT the Severity enum — kept as-is so an off-vocabulary
// label survives to the orchestrator, which normalizes it there.
type verifiedFinding struct {
	Title             string  `json:"title"`
	Verified          bool    `json:"verified"`
	ActualBehavior    string  `json:"actual_behavior"`
	RevisedSeverity   string  `json:"revised_severity"`
	RevisedConfidence float64 `json:"revised_confidence"`
	VerificationNotes string  `json:"verification_notes"`
}

func (v *verifiedFinding) UnmarshalJSON(b []byte) error {
	*v = verifiedFinding{Verified: true, RevisedConfidence: 0.5}
	type alias verifiedFinding
	return json.Unmarshal(b, (*alias)(v))
}

// verificationResult ports _VerificationResult.
type verificationResult struct {
	VerifiedFindings []verifiedFinding `json:"verified_findings"`
}

func (v *verificationResult) UnmarshalJSON(b []byte) error {
	*v = verificationResult{VerifiedFindings: []verifiedFinding{}}
	type alias verificationResult
	return json.Unmarshal(b, (*alias)(v))
}

// deepenFinding ports _DeepenFinding (seeds dimension_id="literal-verify",
// dimension_name="Literal-Correctness Verifier", severity="important",
// confidence=0.7).
type deepenFinding struct {
	DimensionID   string           `json:"dimension_id"`
	DimensionName string           `json:"dimension_name"`
	FilePath      string           `json:"file_path"`
	LineStart     int              `json:"line_start"`
	LineEnd       int              `json:"line_end"`
	Severity      schemas.Severity `json:"severity"`
	Title         string           `json:"title"`
	Body          string           `json:"body"`
	Suggestion    *string          `json:"suggestion"`
	Evidence      string           `json:"evidence"`
	Confidence    float64          `json:"confidence"`
	Tags          []string         `json:"tags"`
}

func (d *deepenFinding) UnmarshalJSON(b []byte) error {
	*d = deepenFinding{
		DimensionID:   "literal-verify",
		DimensionName: "Literal-Correctness Verifier",
		Severity:      "important",
		Confidence:    0.7,
		Tags:          []string{},
	}
	type alias deepenFinding
	return json.Unmarshal(b, (*alias)(d))
}

// deepenResult ports _DeepenResult.
type deepenResult struct {
	Findings []deepenFinding `json:"findings"`
}

func (d *deepenResult) UnmarshalJSON(b []byte) error {
	*d = deepenResult{Findings: []deepenFinding{}}
	type alias deepenResult
	return json.Unmarshal(b, (*alias)(d))
}

// obligation ports _Obligation.
type obligation struct {
	ID       string `json:"id"`
	Where    string `json:"where"`     // the changed line/operation that creates the reliance
	ReliesOn string `json:"relies_on"` // the OTHER location/fact to go find and read
	Property string `json:"property"`  // the exact thing that must hold for correctness
}

// obligationsResult ports _ObligationsResult.
type obligationsResult struct {
	Obligations []obligation `json:"obligations"`
}

func (o *obligationsResult) UnmarshalJSON(b []byte) error {
	*o = obligationsResult{Obligations: []obligation{}}
	type alias obligationsResult
	return json.Unmarshal(b, (*alias)(o))
}

// obligationVerdict ports _ObligationVerdict (seeds holds=true,
// severity="important", confidence=0.7).
type obligationVerdict struct {
	Holds      bool             `json:"holds"`
	Title      string           `json:"title"`
	Severity   schemas.Severity `json:"severity"`
	FilePath   string           `json:"file_path"`
	LineStart  int              `json:"line_start"`
	LineEnd    int              `json:"line_end"`
	Body       string           `json:"body"`
	Evidence   string           `json:"evidence"`
	Suggestion *string          `json:"suggestion"`
	Confidence float64          `json:"confidence"`
}

func (o *obligationVerdict) UnmarshalJSON(b []byte) error {
	*o = obligationVerdict{Holds: true, Severity: "important", Confidence: 0.7}
	type alias obligationVerdict
	return json.Unmarshal(b, (*alias)(o))
}
