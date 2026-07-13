package schemas

import "encoding/json"

// This file implements non-zero-default seeding for every struct that has at
// least one field whose pydantic default is not the Go zero value (design §C.1
// seeding table). Go's json.Unmarshal leaves an absent key at the Go zero
// value, whereas pydantic fills the declared default; each struct below gets a
// defaultXxx() constructor (also usable as the deterministic fallback a role
// returns on a harness parse-failure) plus an UnmarshalJSON that seeds the
// default before decoding — so an absent key keeps the default while a present
// key (even false/0/"") overrides it, matching pydantic exactly.
//
// The `type alias X` trick strips X's methods (including UnmarshalJSON) so the
// inner json.Unmarshal does not recurse; nested field types (e.g. Severity,
// BudgetAllocation) keep their own UnmarshalJSON.
//
// Where a struct in this table has a `default_factory=list` field, the
// constructor seeds it to a non-nil empty slice so that unmarshaling "{}" round
// trips to `[]` (never null), honoring the design's "empty slices marshal as []
// where Python default is a list" rule for the structs whose constructors this
// package owns.

// --- input.go ---

func defaultReviewInput() ReviewInput {
	return ReviewInput{
		Depth:          "auto",
		Focus:          "auto",
		OutputFormat:   "github",
		SuggestionMode: "comment",
		MaxReviewDepth: 2,
		IgnorePaths:    []string{},
		Hints:          []string{},
	}
}

// UnmarshalJSON seeds ReviewInput's non-zero pydantic defaults. The budget-cap
// pointers (MaxCostUSD / MaxDurationSeconds) stay nil — they are resolved later
// by config.ResolveBudgetCaps / ReviewConfig.FromInput.
func (r *ReviewInput) UnmarshalJSON(b []byte) error {
	*r = defaultReviewInput()
	type alias ReviewInput
	return json.Unmarshal(b, (*alias)(r))
}

// --- pipeline.go ---

func defaultBudgetAllocation() BudgetAllocation {
	return BudgetAllocation{
		MaxCostUSD:          0.5,
		MaxDurationSeconds:  60,
		MaxReferenceFollows: 3,
		MaxChildSpawns:      2,
	}
}

// UnmarshalJSON seeds BudgetAllocation defaults (0.5 / 60 / 3 / 2).
func (a *BudgetAllocation) UnmarshalJSON(b []byte) error {
	*a = defaultBudgetAllocation()
	type alias BudgetAllocation
	return json.Unmarshal(b, (*alias)(a))
}

func defaultReviewDimension() ReviewDimension {
	return ReviewDimension{
		ContextFiles: []string{},
		Priority:     1,
		Budget:       defaultBudgetAllocation(),
	}
}

// UnmarshalJSON seeds ReviewDimension.Priority=1 and a default Budget. When a
// "budget" key is present, BudgetAllocation's own UnmarshalJSON re-seeds it
// before applying the overrides.
func (d *ReviewDimension) UnmarshalJSON(b []byte) error {
	*d = defaultReviewDimension()
	type alias ReviewDimension
	return json.Unmarshal(b, (*alias)(d))
}

func defaultSubReviewRequest() SubReviewRequest {
	return SubReviewRequest{
		ContextFiles: []string{},
		Priority:     1,
	}
}

// UnmarshalJSON seeds SubReviewRequest.Priority=1.
func (s *SubReviewRequest) UnmarshalJSON(b []byte) error {
	*s = defaultSubReviewRequest()
	type alias SubReviewRequest
	return json.Unmarshal(b, (*alias)(s))
}

func defaultReviewFinding() ReviewFinding {
	return ReviewFinding{
		Severity:   DefaultSeverity,
		Confidence: 0.5,
		Tags:       []string{},
	}
}

// UnmarshalJSON seeds ReviewFinding.Confidence=0.5, Severity="suggestion".
func (f *ReviewFinding) UnmarshalJSON(b []byte) error {
	*f = defaultReviewFinding()
	type alias ReviewFinding
	return json.Unmarshal(b, (*alias)(f))
}

func defaultAdversaryResult() AdversaryResult {
	return AdversaryResult{SeverityAdjustment: "none"}
}

// UnmarshalJSON seeds AdversaryResult.SeverityAdjustment="none".
func (a *AdversaryResult) UnmarshalJSON(b []byte) error {
	*a = defaultAdversaryResult()
	type alias AdversaryResult
	return json.Unmarshal(b, (*alias)(a))
}

func defaultMetaDimensionResult() MetaDimensionResult {
	return MetaDimensionResult{Confidence: 0.7}
}

// UnmarshalJSON seeds MetaDimensionResult.Confidence=0.7. Dimensions is a
// required field in Python (no default), so it is not seeded.
func (m *MetaDimensionResult) UnmarshalJSON(b []byte) error {
	*m = defaultMetaDimensionResult()
	type alias MetaDimensionResult
	return json.Unmarshal(b, (*alias)(m))
}

// --- gates.go ---

func defaultCoverageGate() CoverageGate {
	return CoverageGate{
		GapDescriptions: []string{},
		Confident:       true,
	}
}

// UnmarshalJSON seeds CoverageGate.Confident=true.
func (c *CoverageGate) UnmarshalJSON(b []byte) error {
	*c = defaultCoverageGate()
	type alias CoverageGate
	return json.Unmarshal(b, (*alias)(c))
}

// --- output.go ---

func defaultScoredFinding() ScoredFinding {
	return ScoredFinding{
		DiffSide:          "RIGHT",
		Severity:          DefaultSeverity,
		Confidence:        0.5,
		Tags:              []string{},
		ActiveMultipliers: []string{},
	}
}

// UnmarshalJSON seeds ScoredFinding.DiffSide="RIGHT", Confidence=0.5,
// Severity="suggestion".
func (s *ScoredFinding) UnmarshalJSON(b []byte) error {
	*s = defaultScoredFinding()
	type alias ScoredFinding
	return json.Unmarshal(b, (*alias)(s))
}

func defaultGitHubComment() GitHubComment {
	return GitHubComment{Side: "RIGHT"}
}

// UnmarshalJSON seeds GitHubComment.Side="RIGHT".
func (c *GitHubComment) UnmarshalJSON(b []byte) error {
	*c = defaultGitHubComment()
	type alias GitHubComment
	return json.Unmarshal(b, (*alias)(c))
}
