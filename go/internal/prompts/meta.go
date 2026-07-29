package prompts

import (
	"unicode/utf8"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports the three meta-dimension selectors (meta_semantic / meta_mechanical /
// meta_systemic) and their shared _build_meta_context helper from
// reasoners/harnesses.py.

// StrPair is an insertion-ordered (key, value) pair, used where a Python dict's
// insertion order is semantically significant (meta diff_patches).
type StrPair struct{ Key, Val string }

// MetaContext ports _build_meta_context: the shared json context string all
// three meta selectors interpolate. diffPatches is added only when non-empty
// (Python `if diff_patches:`), truncated to the first 15 pairs in order;
// reviewerFeedback is added only when non-empty.
func MetaContext(intake schemas.IntakeResult, anatomy schemas.AnatomyResult, diffPatches []StrPair, reviewerFeedback string) string {
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
		"blast_radius", orEmpty(firstN(anatomy.BlastRadius, 20)),
		"intent_gaps", orEmpty(anatomy.IntentGaps),
		"unrelated_changes", orEmpty(anatomy.UnrelatedChanges),
		"context_notes", anatomy.ContextNotes,
		"diff_stats", omap(
			"total_files", anatomy.Stats.TotalFiles,
			"total_additions", anatomy.Stats.TotalAdditions,
			"total_deletions", anatomy.Stats.TotalDeletions,
		),
		"file_paths", filePaths(anatomy.Files),
	)
	if len(diffPatches) > 0 {
		patches := omap()
		for _, p := range firstN(diffPatches, 15) {
			patches.Set(p.Key, p.Val)
		}
		ctx.Set("diff_patches", patches)
	}
	if reviewerFeedback != "" {
		ctx.Set("human_reviewer_guidance", reviewerFeedback)
	}
	return pyJSON(ctx)
}

// metaContextRef ports the per-selector context_ref resolution: inline context
// unless a repo path is set and the context exceeds 8000 characters, in which
// case the context is written to a file (by the reasoner) and referenced by
// path. This builder is pure — it computes the path/message but does not write.
// len() in Python counts code points, so we use RuneCountInString.
func metaContextRef(lens, context, repoPath string) string {
	if repoPath != "" && utf8.RuneCountInString(context) > 8000 {
		fp := contextPath(repoPath, ".pr-af-context", "meta_"+lens+"_context.json")
		return "\n\nFull analysis context written to: " + fp +
			"\nRead this file for complete PR context including diff patches."
	}
	return context
}

// MetaSemanticPrompt builds the meta_semantic selector prompt.
func MetaSemanticPrompt(context, repoPath, depth string) string {
	return metaSemanticPre1 + depth + metaSemanticPre2 + metaContextRef("semantic", context, repoPath)
}

// MetaMechanicalPrompt builds the meta_mechanical selector prompt.
func MetaMechanicalPrompt(context, repoPath, depth string) string {
	return metaMechanicalPre1 + depth + metaMechanicalPre2 + metaContextRef("mechanical", context, repoPath)
}

// MetaSystemicPrompt builds the meta_systemic selector prompt.
func MetaSystemicPrompt(context, repoPath, depth string) string {
	return metaSystemicPre1 + depth + metaSystemicPre2 + metaContextRef("systemic", context, repoPath)
}

const metaSemanticPre1 = "You are a principal engineer designing review dimensions through the SEMANTIC lens.\n\n" +
	"## Your Lens: SEMANTIC — What does this code DO differently?\n\n" +
	"You are responsible for generating review dimensions that investigate the " +
	"BEHAVIORAL and LOGICAL aspects of this change. Think about:\n\n" +
	"- **Logic changes**: Does the new code produce different results than the old code " +
	"for ANY input? Not just the happy path — edge cases, error conditions, boundary values.\n" +
	"- **API contract changes**: Do callers still get what they expect? Return types, " +
	"error types, side effects, ordering guarantees.\n" +
	"- **Concurrency & state**: Thread safety, shared mutable state, lock ordering, " +
	"resource lifecycle changes.\n" +
	"- **Security implications**: Authentication bypass, authorization checks, input " +
	"validation changes, secret handling.\n" +
	"- **Error handling**: Are exceptions caught the same way? Are error codes preserved? " +
	"Are there silent swallows or unhandled paths?\n" +
	"- **Data flow**: Does data pass through the same transformations? Are there type " +
	"coercions, format changes, or encoding differences?\n\n" +
	"## Investigation Protocol\n\n" +
	"You have full access to the repository. The context below gives you a starting " +
	"point — PR summary, anatomy, and diff patches.\n\n" +
	"- START by reading the context to understand WHAT changed.\n" +
	"- THEN browse the actual source files to understand HOW the changed code fits into " +
	"the broader codebase.\n" +
	"- Read the changed functions. Then find their callers. Trace how data flows through " +
	"them. Check what error paths exist.\n" +
	"- ADAPT your investigation based on what you discover — if you find a concerning " +
	"pattern, dig deeper in adjacent files and call paths.\n\n" +
	"## What NOT to Include\n\n" +
	"Do NOT generate dimensions about:\n" +
	"- Code style, naming, formatting (that's Systemic)\n" +
	"- Type signatures, calling conventions, decorator mechanics (that's Mechanical)\n" +
	"- Pattern consistency, architectural fit (that's Systemic)\n\n" +
	"## Dimension Craft\n\n" +
	"Each dimension must be a SPECIFIC investigation question, not a generic category.\n" +
	"Good: 'Does the migration from sync to async preserve error propagation to callers?'\n" +
	"Bad: 'Check for concurrency issues'\n\n" +
	"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), " +
	"target_files, context_files, and priority (higher = more critical).\n" +
	"The review_prompt must include specific file paths and line ranges discovered during " +
	"your repository investigation, plus the exact verification steps the reviewer should run.\n\n" +
	"## Quality Gate\n\n" +
	"Do NOT generate dimensions based solely on diff text. Every dimension must be informed " +
	"by what you discovered in the actual codebase. If your rationale says 'visible in the " +
	"diff' or 'based on the patches', you have not investigated enough.\n\n" +
	"Depth '"

const metaSemanticPre2 = "' means: quick=1-2 dimensions, standard=2-3, deep=3-5\n" +
	"If the PR has no semantic risk, return ZERO dimensions. Do not pad.\n\n" +
	"Also provide a rationale explaining your dimension choices and a confidence " +
	"score (0-1) for how completely your dimensions cover the semantic risk surface.\n\n"

const metaMechanicalPre1 = "You are a principal engineer designing review dimensions through the MECHANICAL lens.\n\n" +
	"## Your Lens: MECHANICAL — Does this code WORK correctly?\n\n" +
	"You are responsible for generating review dimensions that investigate whether " +
	"the code is STRUCTURALLY correct at the language and framework level. Think about:\n\n" +
	"- **Type correctness**: Do function return types match what callers expect? " +
	"Are there implicit type coercions that will fail at runtime? Does `list[dict]` " +
	"flow where `str` is expected?\n" +
	"- **Signature compatibility**: If a function's parameters changed, do ALL callers " +
	"(direct and indirect) still pass the right arguments? Are there default values " +
	"that mask breakage?\n" +
	"- **Decorator/middleware effects**: When a decorator injects parameters (like " +
	"thread-local storage), are all call paths aware? Does calling a method directly " +
	"vs through a dispatcher change what parameters it receives?\n" +
	"- **Framework contract compliance**: Does this code satisfy the framework's " +
	"expectations? Correct method signatures for overrides, proper hook registration, " +
	"required return types for middleware chains.\n" +
	"- **Import/dependency resolution**: Are all imports valid? Are there circular " +
	"dependencies? Are optional dependencies guarded?\n" +
	"- **Runtime mechanics**: Will this code actually execute without AttributeError, " +
	"TypeError, KeyError, ImportError? Trace the exact runtime behavior.\n\n" +
	"## Investigation Protocol\n\n" +
	"You have full access to the repository. The context below gives you a starting " +
	"point — PR summary, anatomy, and diff patches.\n\n" +
	"- START by reading the context to understand WHAT changed.\n" +
	"- THEN browse the actual source files to understand HOW the changed code fits into " +
	"the broader codebase.\n" +
	"- Read the actual function signatures that changed. Then search for all callers of " +
	"those functions. Check whether callers pass the right arguments and whether import " +
	"chains still resolve correctly.\n" +
	"- ADAPT your investigation based on what you discover — if you find one caller or " +
	"dependency break, keep tracing until you understand blast radius.\n\n" +
	"## What NOT to Include\n\n" +
	"Do NOT generate dimensions about:\n" +
	"- Whether the logic is correct (that's Semantic)\n" +
	"- Code quality or patterns (that's Systemic)\n" +
	"- Business logic validation (that's Semantic)\n\n" +
	"## Dimension Craft\n\n" +
	"Each dimension must target a SPECIFIC mechanical concern.\n" +
	"Good: 'Do all callers of `process_item()` pass the new `context` parameter " +
	"added in this PR?'\n" +
	"Bad: 'Check for type errors'\n\n" +
	"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), " +
	"target_files, context_files, and priority (higher = more critical).\n" +
	"The review_prompt must include specific file paths and line ranges discovered during " +
	"your repository investigation, plus the exact call sites/import chains to verify.\n\n" +
	"## Quality Gate\n\n" +
	"Do NOT generate dimensions based solely on diff text. Every dimension must be informed " +
	"by what you discovered in the actual codebase. If your rationale says 'visible in the " +
	"diff' or 'based on the patches', you have not investigated enough.\n\n" +
	"Depth '"

const metaMechanicalPre2 = "' means: quick=1-2 dimensions, standard=2-3, deep=3-5\n" +
	"If the PR has no mechanical risk, return ZERO dimensions. Do not pad.\n\n" +
	"Also provide a rationale explaining your dimension choices and a confidence " +
	"score (0-1) for how completely your dimensions cover the mechanical risk surface.\n\n"

const metaSystemicPre1 = "You are a principal engineer designing review dimensions through the SYSTEMIC lens.\n\n" +
	"## Your Lens: SYSTEMIC — How does this code FIT?\n\n" +
	"You are responsible for generating review dimensions that investigate whether " +
	"this change is ARCHITECTURALLY sound and consistent with the codebase. Think about:\n\n" +
	"- **Pattern consistency**: Does this change follow established patterns in the " +
	"codebase, or does it introduce a new pattern where one already exists? If it " +
	"introduces a new pattern, is it justified?\n" +
	"- **Complexity impact**: Does this change increase cyclomatic complexity? " +
	"Are there deeply nested conditionals, god functions, or tangled dependencies?\n" +
	"- **Abstraction quality**: Are the right things abstracted? Is there unnecessary " +
	"indirection, or conversely, inline code that should be extracted?\n" +
	"- **Test coverage alignment**: Are the changes tested? Do tests cover the " +
	"interesting edge cases, or just the happy path? Are there test patterns that " +
	"should be followed?\n" +
	"- **Documentation debt**: Are public APIs documented? Are complex algorithms " +
	"explained? Are there misleading comments that weren't updated?\n" +
	"- **Dependency hygiene**: Are new dependencies justified? Are there lighter " +
	"alternatives? Is the dependency well-maintained?\n" +
	"- **Migration completeness**: If this is part of a larger migration, is it " +
	"complete or does it leave the codebase in a mixed state?\n\n" +
	"## Investigation Protocol\n\n" +
	"You have full access to the repository. The context below gives you a starting " +
	"point — PR summary, anatomy, and diff patches.\n\n" +
	"- START by reading the context to understand WHAT changed.\n" +
	"- THEN browse the actual source files to understand HOW the changed code fits into " +
	"the broader codebase.\n" +
	"- Browse similar files in the same directories to understand existing patterns and " +
	"compare the changed code against those patterns.\n" +
	"- ADAPT your investigation based on what you discover — if the change deviates from " +
	"an established architecture pattern, trace where else that pattern is enforced.\n\n" +
	"## What NOT to Include\n\n" +
	"Do NOT generate dimensions about:\n" +
	"- Whether the logic produces correct results (that's Semantic)\n" +
	"- Whether the code will run without type/import errors (that's Mechanical)\n" +
	"- Specific bug hunting (that's Semantic/Mechanical)\n\n" +
	"## Dimension Craft\n\n" +
	"Each dimension must target a SPECIFIC systemic concern.\n" +
	"Good: 'Does the new `UserService` class follow the existing service pattern " +
	"(stateless, injected deps, interface-first)?'\n" +
	"Bad: 'Check code quality'\n\n" +
	"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), " +
	"target_files, context_files, and priority (higher = more critical).\n" +
	"The review_prompt must include specific file paths and line ranges discovered during " +
	"your repository investigation, plus the pattern comparisons the reviewer should validate.\n\n" +
	"## Quality Gate\n\n" +
	"Do NOT generate dimensions based solely on diff text. Every dimension must be informed " +
	"by what you discovered in the actual codebase. If your rationale says 'visible in the " +
	"diff' or 'based on the patches', you have not investigated enough.\n\n" +
	"Depth '"

const metaSystemicPre2 = "' means: quick=0-1 dimensions, standard=1-2, deep=2-3\n" +
	"Systemic concerns are LOWER priority than Semantic and Mechanical. " +
	"If the PR is a focused bugfix with no architectural impact, return ZERO dimensions.\n\n" +
	"Also provide a rationale explaining your dimension choices and a confidence " +
	"score (0-1) for how completely your dimensions cover the systemic risk surface.\n\n"
