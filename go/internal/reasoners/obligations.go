package reasoners

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// ExtractObligations ports extract_obligations: enumerate the cross-location
// consistency obligations the changed code creates (breadth pass; judged one
// at a time by VerifyObligation).
//
// Output keys (§B.2): obligations (each: id, where, relies_on, property). No
// non-empty patches short-circuits to an empty list without a harness call;
// parse failure degrades to an empty list.
func ExtractObligations(ctx context.Context, deps Deps, in ExtractObligationsInput) (map[string]any, error) {
	patches := filterPairs(in.DiffPatches)
	if len(patches) == 0 {
		return map[string]any{"obligations": []any{}}, nil
	}

	// The builder embeds a file reference when the rendered patches exceed
	// 9000 characters and a repo path exists; the write is the reasoner's job.
	patchesText := renderPatches(patches)
	if utf8.RuneCountInString(patchesText) > 9000 && in.RepoPath != "" {
		if _, err := writeContextFile(patchesText, "obligations_diff.md", in.RepoPath); err != nil {
			return nil, err
		}
	}

	prompt := prompts.ExtractObligationsPrompt([]prompts.StrPair(in.DiffPatches), in.RepoPath, in.PrContext)
	parsed, _, err := harnessx.Run[obligationsResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	obligations, err := dumpSlice(parsed.Obligations)
	if err != nil {
		return nil, err
	}
	return map[string]any{"obligations": obligations}, nil
}

// VerifyObligation ports verify_obligation: read BOTH ends of one consistency
// obligation and decide whether the property holds.
//
// Output keys (§B.2): holds, title, severity, file_path, line_start, line_end,
// body, evidence, suggestion, confidence. Parse failure degrades to the seeded
// verdict (holds=true, severity="important", confidence=0.7) — Python's
// `_ObligationVerdict(holds=True)`.
func VerifyObligation(ctx context.Context, deps Deps, in VerifyObligationInput) (map[string]any, error) {
	// Python: `_Obligation.model_validate(obligation)`.
	var o obligation
	b, err := json.Marshal(in.Obligation)
	if err != nil {
		return nil, fmt.Errorf("reasoners: verify_obligation input: %w", err)
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("reasoners: verify_obligation input: %w", err)
	}

	prompt := prompts.VerifyObligationPrompt(o.Where, o.ReliesOn, o.Property)
	parsed, _, err := harnessx.Run[obligationVerdict](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	return dumpMap(*parsed)
}
