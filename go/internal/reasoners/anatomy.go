package reasoners

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/blastradius"
	"github.com/Agent-Field/pr-af/go/internal/diffengine"
	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// AnatomyPhase ports anatomy_phase: deterministic diff decomposition (parse,
// stats, clusters, blast radius) plus one .harness() call for the semantic
// narrative fields, merged into an AnatomyResult.
//
// Output keys (§B.2): files, clusters, blast_radius, dependency_graph, stats,
// pr_narrative, risk_surfaces, unrelated_changes, intent_gaps, context_notes.
// A harness parse failure degrades to empty semantic fields (Python falls back
// to a default _AnatomySemanticResult), never an error.
func AnatomyPhase(ctx context.Context, deps Deps, in AnatomyInput) (map[string]any, error) {
	pr := in.PRData

	files := diffengine.ParseUnifiedDiff(pr.Diff)
	if len(files) == 0 {
		files = fileChangesFromMetadata(pr)
	}

	stats := diffengine.ComputeDiffStats(files)
	clusters := diffengine.ClusterChanges(files)
	changedPaths := make([]string, len(files))
	for i, f := range files {
		changedPaths[i] = f.Path
	}
	blastRadius := blastradius.ComputeBlastRadius(changedPaths, in.RepoPath)

	prompt := prompts.AnatomyPrompt(
		in.Intake, pr.Title, pr.Description, pr.Labels, clusters, stats, len(blastRadius), files,
	)
	parsed, _, err := harnessx.Run[anatomySemanticResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	// Run's seeded default already matches Python's `_AnatomySemanticResult()`
	// fallback on Parsed==nil; only nil slices from explicit JSON nulls need
	// coercion so model_dump-parity emits [] rather than null.
	semantic := *parsed

	anatomy := schemas.AnatomyResult{
		Files:            files,
		Clusters:         clusters,
		BlastRadius:      orEmptyStrs(blastRadius),
		DependencyGraph:  map[string][]string{},
		Stats:            stats,
		PrNarrative:      semantic.PrNarrative,
		RiskSurfaces:     orEmptyStrs(semantic.RiskSurfaces),
		UnrelatedChanges: orEmptyStrs(semantic.UnrelatedChanges),
		IntentGaps:       orEmptyStrs(semantic.IntentGaps),
		ContextNotes:     semantic.ContextNotes,
	}
	if anatomy.Files == nil {
		anatomy.Files = []schemas.FileChange{}
	}
	if anatomy.Clusters == nil {
		anatomy.Clusters = []schemas.ChangeCluster{}
	}
	return dumpMap(anatomy)
}
