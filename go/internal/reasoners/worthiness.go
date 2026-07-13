package reasoners

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// PostWorthinessGate ports post_worthiness_gate: an experienced-reviewer
// judgment over which findings are genuinely worth posting (recall-preserving:
// unsure -> keep).
//
// Output keys (§B.2): keep_indices, reasoning — EXCEPT the <=1-finding
// short-circuit, which Python returns as {"keep_indices": [...]} with no
// reasoning key. Empty/invalid harness indices (including parse failure)
// degrade to keep-everything, never silencing all findings.
func PostWorthinessGate(ctx context.Context, deps Deps, in PostWorthinessInput) (map[string]any, error) {
	n := len(in.Findings)
	if n <= 1 {
		// Python: `return {"keep_indices": list(range(len(findings)))}` — no
		// reasoning key on this branch.
		return map[string]any{"keep_indices": rangeInts(n)}, nil
	}

	// The committed prompt builder is typed over ScoredFinding; the live path
	// feeds ReviewFinding dumps. The prompt reads only the fields the two
	// shapes share (severity, file_path, line_start, title, body, evidence),
	// so a straight field copy is exact.
	scored := make([]schemas.ScoredFinding, n)
	for i, f := range in.Findings {
		scored[i] = schemas.ScoredFinding{
			Severity:  f.Severity,
			FilePath:  f.FilePath,
			LineStart: f.LineStart,
			Title:     f.Title,
			Body:      f.Body,
			Evidence:  f.Evidence,
		}
	}
	prompt := prompts.PostWorthinessPrompt(scored)

	parsed, _, err := harnessx.Run[postWorthinessResult](ctx, deps.Harness, prompt, harness.Options{})
	if err != nil {
		return nil, err
	}

	keep := []int{}
	for _, i := range parsed.KeepIndices {
		if i >= 0 && i < n {
			keep = append(keep, i)
		}
	}
	if len(keep) == 0 { // never silence everything on a parse/judgment failure
		keep = rangeInts(n)
	}
	return map[string]any{"keep_indices": keep, "reasoning": parsed.Reasoning}, nil
}
