package reasoners

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// IntakePhase ports intake_phase: a fast .ai() metadata classification gate
// that, when confident, combines with the deterministic extractors into an
// IntakeResult; otherwise it escalates to a full .harness() classification.
//
// Output keys (§B.2): pr_type, complexity, languages, areas_touched,
// risk_signals, ai_generated, review_depth, pr_summary — or {} when the
// harness fallback fails to parse (Python returns a literal empty dict).
func IntakePhase(ctx context.Context, deps Deps, in IntakeInput) (map[string]any, error) {
	pr := in.PRData
	filesChanged := len(pr.ChangedFiles)
	languages := extractLanguages(pr)

	gatePrompt := prompts.IntakeGatePrompt(
		pr.Title, pr.Description, pr.Labels, pr.Author, filesChanged, languages, pr.CommitMessages,
	)
	var gate schemas.IntakeGate
	if err := aiStructured(ctx, deps.AI, gatePrompt, prompts.IntakeGateSystem, strictAISchemas[strictAISchemaIntakeGate], &gate); err != nil {
		return nil, err
	}

	if gate.Confident {
		paths := make([]string, len(pr.ChangedFiles))
		for i, changed := range pr.ChangedFiles {
			paths[i] = changed.Path
		}
		areasTouched := extractAreas(paths)
		reviewDepth := in.Depth
		if in.Depth == "auto" {
			reviewDepth = autoDepth(gate.Complexity)
		}
		intake := schemas.IntakeResult{
			PrType:       gate.PrType,
			Complexity:   gate.Complexity,
			Languages:    languages,
			AreasTouched: areasTouched,
			RiskSignals:  riskSignals(pr, areasTouched, filesChanged),
			AIGenerated:  aiGeneratedConfidence(pr),
			ReviewDepth:  reviewDepth,
			PrSummary:    prSummary(pr),
		}
		return dumpMap(intake)
	}

	fallbackPrompt := prompts.IntakeFallbackPrompt(pr.Title, pr.Description, in.Depth, languages, filesChanged)
	parsed, res, err := harnessx.Run[schemas.IntakeResult](ctx, deps.Harness, fallbackPrompt, harness.Options{})
	if err != nil {
		return nil, err
	}
	if res == nil || res.Parsed == nil {
		// Python: `fallback_result.parsed.model_dump() if fallback_result.parsed else {}`.
		return map[string]any{}, nil
	}
	out := *parsed
	out.Languages = orEmptyStrs(out.Languages)
	out.AreasTouched = orEmptyStrs(out.AreasTouched)
	out.RiskSignals = orEmptyStrs(out.RiskSignals)
	return dumpMap(out)
}
