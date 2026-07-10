package reasoners

import (
	"context"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// CoverageGate ports coverage_gate: one .ai() call that decides whether the
// review dimensions covered every change cluster.
//
// Output keys (§B.2): fully_covered, gap_descriptions, confident. Absent
// response keys land on the pydantic defaults through CoverageGate's seeded
// UnmarshalJSON (confident=true, gap_descriptions=[]); an AI transport or
// decode error propagates exactly as Python lets the exception escape.
func CoverageGate(ctx context.Context, deps Deps, in CoverageGateInput) (map[string]any, error) {
	prompt := prompts.CoverageGatePrompt(in.Anatomy, in.ReviewedClusters, in.DimensionNamesReviewed)

	var gate schemas.CoverageGate
	if err := aiStructured(ctx, deps.AI, prompt, prompts.CoverageGateSystem, schemas.CoverageGate{}, &gate); err != nil {
		return nil, err
	}
	gate.GapDescriptions = orEmptyStrs(gate.GapDescriptions)
	return dumpMap(gate)
}
