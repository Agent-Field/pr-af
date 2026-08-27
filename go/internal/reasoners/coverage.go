package reasoners

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// CoverageGate ports coverage_gate: one .ai() call that decides whether the
// review dimensions covered every change cluster, with a .harness() retry of
// the same prompt when the AI seam is unavailable or refuses the structured
// request (harnesses.py coverage_gate wraps its .ai() call the same way).
//
// Output keys (§B.2): fully_covered, gap_descriptions, confident. Absent
// response keys land on the pydantic defaults through CoverageGate's seeded
// UnmarshalJSON (confident=true, gap_descriptions=[]). A missing structured
// harness fallback fails the phase.
func CoverageGate(ctx context.Context, deps Deps, in CoverageGateInput) (map[string]any, error) {
	prompt := prompts.CoverageGatePrompt(in.Anatomy, in.ReviewedClusters, in.DimensionNamesReviewed)

	var gate schemas.CoverageGate
	if err := aiStructured(ctx, deps.AI, prompt, prompts.CoverageGateSystem, strictAISchemas[strictAISchemaCoverageGate], &gate); err != nil {
		parsed, _, harnessErr := harnessx.Run[schemas.CoverageGate](ctx, deps.Harness, prompt, harness.Options{})
		if harnessErr != nil {
			return nil, harnessErr
		}
		gate = *parsed
	}
	gate.GapDescriptions = orEmptyStrs(gate.GapDescriptions)
	return dumpMap(gate)
}
