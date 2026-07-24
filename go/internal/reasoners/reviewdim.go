package reasoners

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// ReviewDimension ports review_dimension: one focused reviewer over an
// assigned dimension, with optional sub-review spawning below max depth.
//
// Output keys (§B.2): findings, sub_reviews, current_depth. A schema parse
// failure logs loudly (indistinguishable from a clean review otherwise) and
// reports zero findings — Python prints and substitutes an empty
// _ReviewFindingsResult.
func ReviewDimension(ctx context.Context, deps Deps, in ReviewDimensionInput) (map[string]any, error) {
	canSpawn := in.CurrentDepth < in.MaxDepth

	// The prompt builder embeds file references for oversized diff/primed
	// sections under these exact conditions; the writes are the reasoner's job.
	if len(in.DiffPatches) > 0 {
		var parts []string
		for _, path := range in.TargetFiles {
			if patch, ok := in.DiffPatches[path]; ok && patch != "" {
				parts = append(parts, "### "+path+"\n```diff\n"+patch+"\n```")
			}
		}
		if len(parts) > 0 {
			patchesText := strings.Join(parts, "\n\n")
			if in.RepoPath != "" && utf8.RuneCountInString(patchesText) > 6000 {
				if _, err := writeContextFile(patchesText, "review_dimension_diff_patches.md", in.RepoPath); err != nil {
					return nil, err
				}
			}
		}
	}
	if in.PrimedCode != "" && in.RepoPath != "" && utf8.RuneCountInString(in.PrimedCode) > 6000 {
		if _, err := writeContextFile(in.PrimedCode, "review_dimension_primed_code.md", in.RepoPath); err != nil {
			return nil, err
		}
	}

	prompt := prompts.ReviewDimensionPrompt(prompts.ReviewDimensionOptions{
		ReviewPrompt:      in.ReviewPrompt,
		TargetFiles:       in.TargetFiles,
		ContextFiles:      in.ContextFiles,
		RepoPath:          in.RepoPath,
		CurrentDepth:      in.CurrentDepth,
		MaxDepth:          in.MaxDepth,
		PrNarrative:       in.PrNarrative,
		RiskSurfaces:      in.RiskSurfaces,
		IntakeSummary:     in.IntakeSummary,
		DiffPatches:       in.DiffPatches,
		AllDimensionNames: in.AllDimensionNames,
		ReviewerFeedback:  in.ReviewerFeedback,
		PrimedCode:        in.PrimedCode,
	})

	parsed, res, err := harnessx.Run[reviewFindingsResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	schemaParseFailed := res == nil || res.Parsed == nil
	if schemaParseFailed {
		// Schema parse failed entirely — don't silently report "0 findings",
		// which is indistinguishable from a clean review. Make it visible.
		errMsg := "None"
		if res != nil && res.ErrorMessage != "" {
			errMsg = "'" + res.ErrorMessage + "'"
		}
		fmt.Printf(
			"[PR-AF] review_dimension: schema parse failed — treating as 0 findings for this dimension (error=%s)\n",
			errMsg,
		)
	}
	result := *parsed
	if schemaParseFailed {
		result.Findings = nil
		result.SubReviews = nil
	}
	for i := range result.Findings {
		result.Findings[i].Tags = orEmptyStrs(result.Findings[i].Tags)
	}

	subReviewDicts := []any{}
	if canSpawn && len(result.SubReviews) > 0 {
		// Python: `for sr in parsed.sub_reviews[:2] if sr.review_prompt and
		// sr.target_files` — slice first, then filter.
		for _, sr := range capN(result.SubReviews, 2) {
			if sr.ReviewPrompt == "" || len(sr.TargetFiles) == 0 {
				continue
			}
			subReviewDicts = append(subReviewDicts, map[string]any{
				"reason":        sr.Reason,
				"review_prompt": sr.ReviewPrompt,
				"target_files":  orEmptyStrs(sr.TargetFiles),
				"context_files": orEmptyStrs(sr.ContextFiles),
				"priority":      sr.Priority,
			})
		}
	}

	findings, err := dumpSlice(result.Findings)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"findings":            findings,
		"sub_reviews":         subReviewDicts,
		"current_depth":       in.CurrentDepth,
		"schema_parse_failed": schemaParseFailed,
	}, nil
}
