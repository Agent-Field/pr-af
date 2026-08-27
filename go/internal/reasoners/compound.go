package reasoners

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// CompoundFinderPhase ports compound_finder_phase: given a small cluster of
// findings that might interact, synthesize NEW compound findings when the
// combined risk is real.
//
// Output keys (§B.2): findings. Fewer than two findings short-circuits to an
// empty list without a harness call; missing or unparseable structured output
// fails the required cross-reference phase.
func CompoundFinderPhase(ctx context.Context, deps Deps, in CompoundFinderInput) (map[string]any, error) {
	if len(in.ClusterFindings) < 2 {
		return map[string]any{"findings": []any{}}, nil
	}

	evMap := evidenceOMaps(in.EvidenceMap)

	// The builder embeds a file reference when the context JSON exceeds 10000
	// characters and a repo path exists; the write is the reasoner's job. The
	// content is the exact json.dumps payload the builder renders inline
	// otherwise (pinned by contexts_test.go).
	summary := compoundFinderContext(in.ClusterFindings, evMap)
	if utf8.RuneCountInString(summary) > 10000 && in.RepoPath != "" {
		if _, err := writeContextFile(summary, "compound_cluster_findings.json", in.RepoPath); err != nil {
			return nil, err
		}
	}

	prompt := prompts.CompoundFinderPrompt(in.ClusterFindings, in.RepoPath, evMap)
	parsed, _, err := harnessx.Run[compoundResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	result := *parsed
	for i := range result.Findings {
		result.Findings[i].Tags = orEmptyStrs(result.Findings[i].Tags)
		result.Findings[i].ContributingFindings = orEmptyStrs(result.Findings[i].ContributingFindings)
	}
	findings, err := dumpSlice(result.Findings)
	if err != nil {
		return nil, err
	}
	return map[string]any{"findings": findings}, nil
}

// CompoundDedupPhase ports compound_dedup_phase: one harness call that decides
// which compound findings are genuinely distinct insights.
//
// Output keys (§B.2): keep_indices, reasoning. One-or-fewer findings
// short-circuits to keep-everything; an empty/invalid index list (including
// the parse-failure fallback) also degrades to keep-everything.
func CompoundDedupPhase(ctx context.Context, deps Deps, in CompoundDedupInput) (map[string]any, error) {
	n := len(in.CompoundFindings)
	if n <= 1 {
		return map[string]any{
			"keep_indices": rangeInts(n),
			"reasoning":    "single finding, no dedup needed",
		}, nil
	}

	prompt := prompts.CompoundDedupPrompt(in.CompoundFindings, in.IndividualFindingsSummary)
	parsed, _, err := harnessx.Run[compoundDedupResult](ctx, deps.Harness, prompt, harness.Options{})
	if err != nil {
		var structuredErr *harnessx.StructuredOutputError
		if !errors.As(err, &structuredErr) {
			return nil, err
		}
		return map[string]any{"keep_indices": rangeInts(n), "reasoning": ""}, nil
	}

	valid := []int{}
	for _, i := range parsed.KeepIndices {
		if i >= 0 && i < n {
			valid = append(valid, i)
		}
	}
	if len(valid) == 0 {
		// Fallback: keep all if the harness returned nothing valid.
		valid = rangeInts(n)
	}
	return map[string]any{"keep_indices": valid, "reasoning": parsed.Reasoning}, nil
}

// rangeInts reproduces Python's list(range(n)).
func rangeInts(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
