package prompts

import "github.com/Agent-Field/pr-af/go/internal/schemas"

// Ports the anatomy_phase .harness() prompt from reasoners/harnesses.py.

const anatomyPreamble = "You are a senior engineer performing structural analysis of a pull request before " +
	"review dimensions are assigned. Your job is NOT to find bugs yet — it is to deeply " +
	"understand WHAT changed, WHY it changed, and WHERE the risk surfaces are.\n\n" +
	"Think like an architect reviewing a change set:\n\n" +
	"1. **PR Narrative**: Write a clear technical narrative of what this PR actually does " +
	"(not what the PR description says — what the CODE says). Trace the change from " +
	"entry point to effect. If the PR replaces one mechanism with another, describe both " +
	"the old and new mechanisms and where they differ.\n\n" +
	"2. **Risk Surfaces**: Identify areas where this change could break things that are " +
	"NOT obvious from the diff alone. Think about:\n" +
	"   - Callers of changed functions/methods that might pass arguments differently\n" +
	"   - Implicit contracts (ordering, timing, state) that the change might violate\n" +
	"   - Error paths — if the old code handled errors one way, does the new code preserve that?\n" +
	"   - Concurrency: thread safety, shared state, decorator-injected arguments\n" +
	"   - API boundaries: do callers still get what they expect?\n" +
	"   - Configuration/defaults that changed (especially security-sensitive ones)\n\n" +
	"3. **Unrelated Changes**: Flag anything that doesn't belong in this PR's stated intent.\n\n" +
	"4. **Intent Gaps**: Where does the code diverge from what the PR description promises? " +
	"Where is the PR description silent about something the code actually does?\n\n" +
	"Be specific. Name files, functions, and line ranges. A vague risk surface is useless.\n\n"

// AnatomyPrompt builds the anatomy_phase prompt. clusters/stats/blastRadiusCount/
// files are the deterministic diff-engine outputs (T2.1) the reasoner computed;
// they are passed in typed so this builder never touches diff parsing.
func AnatomyPrompt(intake schemas.IntakeResult, prTitle, prDescription string, prLabels []string, clusters []schemas.ChangeCluster, stats schemas.DiffStats, blastRadiusCount int, files []schemas.FileChange) string {
	ctx := omap(
		"intake", omap(
			"pr_type", intake.PrType,
			"complexity", intake.Complexity,
			"pr_summary", intake.PrSummary,
		),
		"pr_metadata", omap(
			"title", prTitle,
			"description", delimitPRDescription(runeSlice(prDescription, 4000)),
			"labels", orEmpty(prLabels),
		),
		"clusters", clusterDescriptions(clusters),
		"stats", statsOMap(stats),
		"blast_radius_count", blastRadiusCount,
		"files_changed", fileChangeOMaps(files),
	)
	return anatomyPreamble + pyJSON(ctx)
}
