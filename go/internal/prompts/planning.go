package prompts

import "github.com/Agent-Field/pr-af/go/internal/schemas"

// Ports the planning_phase .harness() prompt from reasoners/harnesses.py.
// (Registered but dead on the live path; ported for completeness.)

const planningPre1 = "You are a principal engineer designing a review strategy for a pull request. " +
	"Your job is to decompose this PR into review DIMENSIONS — each one a focused, " +
	"independently-executable investigation that another senior engineer will carry out.\n\n" +
	"DO NOT use generic templates like 'security review' or 'performance review'. " +
	"Every dimension must be SPECIFIC to what THIS PR actually changes.\n\n" +
	"## How to Think About Dimensions\n\n" +
	"A dimension is NOT 'check file X for bugs'. A dimension is a specific QUESTION about " +
	"the change that requires reading code to answer. Good dimensions:\n\n" +
	"- 'Does the migration from library A to library B preserve error semantics?' " +
	"(target: the wrapper functions; context: the callers)\n" +
	"- 'Are all callers of method X updated to match its new signature?' " +
	"(target: the callers; context: the method definition)\n" +
	"- 'Does the new default value for config Y break existing deployments?' " +
	"(target: where Y is consumed; context: where Y is defined and documented)\n" +
	"- 'Can the refactored data flow produce states that the old flow could not?' " +
	"(target: state transitions; context: consumers of that state)\n\n" +
	"Bad dimensions: 'Review security', 'Check for bugs', 'Validate tests'\n\n" +
	"## Dimension Categories to Consider\n\n" +
	"Not all will apply — generate ONLY what matters for THIS PR:\n\n" +
	"1. **Behavioral Equivalence**: When code is refactored or a dependency is swapped, " +
	"does the new code behave identically in all paths? Edge cases, error handling, " +
	"return types, side effects, timing.\n\n" +
	"2. **Contract Preservation**: Are function signatures, decorator behaviors, " +
	"serialization formats, and API responses preserved? When a decorator adds an " +
	"implicit parameter, are all call sites (direct AND indirect) updated?\n\n" +
	"3. **Cross-Boundary Consistency**: Changes in module A may violate assumptions " +
	"in module B. Look for shared types, constants, configs, or patterns that appear " +
	"in both changed and unchanged files.\n\n" +
	"4. **Error Propagation & Recovery**: Follow every error path. Does the new code " +
	"catch the same exceptions? Raise the same error types? Preserve error codes? " +
	"Avoid swallowing errors that the old code surfaced?\n\n" +
	"5. **State & Concurrency**: Thread-local storage, shared handles, connection " +
	"lifecycle, resource cleanup. Does the change introduce shared mutable state, " +
	"or change who owns a resource?\n\n" +
	"6. **Data Integrity & Migration**: Schema changes, default value changes, " +
	"format changes. Can old data be read by new code? Can new data be read by " +
	"rollback code?\n\n" +
	"7. **Architectural Coherence**: Does this change follow or violate the codebase's " +
	"established patterns? Does it introduce a new pattern where one already exists? " +
	"Does it create technical debt or resolve it?\n\n" +
	"## Review Prompt Craft\n\n" +
	"Each dimension's `review_prompt` will be given to another engineer who will read " +
	"the actual code. Make it a COMPLETE briefing:\n" +
	"- State exactly what to investigate\n" +
	"- Explain what 'correct' looks like\n" +
	"- Point out what subtle failures would look like\n" +
	"- Mention specific functions, classes, or patterns to trace\n\n" +
	"## Cross-Reference Hints\n\n" +
	"Identify specific pairs or groups of findings that could interact. " +
	"Example: 'If dimension A finds that error types changed, AND dimension B finds " +
	"callers that catch specific error types, those interact.'\n\n" +
	"## Output Requirements\n\n" +
	"- Prioritize dimensions by risk (highest first)\n" +
	"- Each dimension has: target_files (to inspect) and context_files (for reference)\n" +
	"- Depth '"

const planningPre2 = "' means: quick=2-3 dimensions, standard=3-5, deep=5-8, thorough=6-10\n" +
	"- If the PR has a narrow scope, fewer dimensions is BETTER than padding with fluff\n\n"

// PlanningPrompt builds the planning_phase prompt.
func PlanningPrompt(intake schemas.IntakeResult, anatomy schemas.AnatomyResult, depth string, hints []string) string {
	ctx := omap(
		"intake", omap(
			"pr_type", intake.PrType,
			"complexity", intake.Complexity,
			"pr_summary", intake.PrSummary,
			"areas_touched", orEmpty(intake.AreasTouched),
			"risk_signals", orEmpty(intake.RiskSignals),
		),
		"clusters", clusterDescriptions(anatomy.Clusters),
		"risk_surfaces", orEmpty(anatomy.RiskSurfaces),
		"pr_narrative", anatomy.PrNarrative,
		"depth", depth,
		"hints", orEmpty(hints),
		"file_paths", filePaths(anatomy.Files),
	)
	return planningPre1 + depth + planningPre2 + pyJSON(ctx)
}
