package schemas

// This file ports the flat .ai() gate schemas from schemas/gates.py that the
// design §C.1 keeps: IntakeGate and CoverageGate. (schemas/gates.py also
// defines DedupGate, which the ported pipeline does not use — the compound
// dedup phase returns keep_indices/reasoning instead — so it is intentionally
// omitted per §C.1.)

// IntakeGate is the fast PR classification. It escalates to .harness() when not
// confident. No non-zero defaults, so not seeded.
type IntakeGate struct {
	PrType     string `json:"pr_type"`    // feature | bugfix | refactor | docs | infra | mixed
	Complexity string `json:"complexity"` // trivial | standard | complex | massive
	Confident  bool   `json:"confident"`  // Whether classification is confident enough
}

// CoverageGate checks whether the review plan covered all change clusters.
// Seeded (defaults.go): confident=true, gap_descriptions=[].
type CoverageGate struct {
	FullyCovered    bool     `json:"fully_covered"`
	GapDescriptions []string `json:"gap_descriptions"`
	Confident       bool     `json:"confident"`
}
