package reasoners

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// PlanningPhase ports planning_phase (registered but DEAD on the live path —
// the meta-selectors replaced it; kept for surface parity).
//
// Output keys (§B.2): dimensions, cross_ref_hints, ai_adjusted, total_budget —
// or the literal {"dimensions": [], "cross_ref_hints": []} on parse failure.
func PlanningPhase(ctx context.Context, deps Deps, in PlanningInput) (map[string]any, error) {
	prompt := prompts.PlanningPrompt(in.Intake, in.Anatomy, in.Depth, in.Hints)
	parsed, res, err := harnessx.Run[schemas.ReviewPlan](ctx, deps.Harness, prompt, harness.Options{})
	if err != nil {
		return nil, err
	}
	if res == nil || res.Parsed == nil {
		// Python: `{"dimensions": [], "cross_ref_hints": []}` — two keys only.
		return map[string]any{
			"dimensions":      []any{},
			"cross_ref_hints": []any{},
		}, nil
	}
	plan := *parsed
	if plan.Dimensions == nil {
		plan.Dimensions = []schemas.ReviewDimension{}
	}
	// Python: total_budget = Field(default_factory=BudgetAllocation) — an
	// absent key gets the seeded default (0.5/60/3/2). schemas.ReviewPlan has
	// no UnmarshalJSON seeding, so an absent total_budget arrives as the zero
	// struct; re-seed it. (A model explicitly emitting all four zeros would be
	// re-seeded too — pydantic would keep them — but Python also re-seeds the
	// far likelier `"total_budget": {}`, and this reasoner is dead on the live
	// path.)
	if plan.TotalBudget == (schemas.BudgetAllocation{}) {
		plan.TotalBudget = schemas.BudgetAllocation{
			MaxCostUSD:          0.5,
			MaxDurationSeconds:  60,
			MaxReferenceFollows: 3,
			MaxChildSpawns:      2,
		}
	}
	plan.CrossRefHints = orEmptyStrs(plan.CrossRefHints)
	for i := range plan.Dimensions {
		plan.Dimensions[i].TargetFiles = orEmptyStrs(plan.Dimensions[i].TargetFiles)
		plan.Dimensions[i].ContextFiles = orEmptyStrs(plan.Dimensions[i].ContextFiles)
	}
	return dumpMap(plan)
}
