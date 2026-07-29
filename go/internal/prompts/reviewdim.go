package prompts

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Ports review_dimension from reasoners/harnesses.py — the richest builder, with
// seven optional sections assembled by Python truthiness plus two "written to
// file" branches.

// ReviewDimensionOptions mirrors the review_dimension reasoner signature.
type ReviewDimensionOptions struct {
	ReviewPrompt      string
	TargetFiles       []string
	ContextFiles      []string
	RepoPath          string
	CurrentDepth      int
	MaxDepth          int
	PrNarrative       string
	RiskSurfaces      []string
	IntakeSummary     string
	DiffPatches       map[string]string
	AllDimensionNames []string
	ReviewerFeedback  string
	PrimedCode        string
}

// ReviewDimensionPrompt builds the review_dimension reviewer prompt.
func ReviewDimensionPrompt(o ReviewDimensionOptions) string {
	canSpawn := o.CurrentDepth < o.MaxDepth

	feedbackSection := ""
	if o.ReviewerFeedback != "" {
		feedbackSection = "## Human Reviewer Guidance (IMPORTANT)\n\n" +
			"A human reviewer saw the previous round of findings and asked for a re-review " +
			"with this guidance:\n\n> " + o.ReviewerFeedback + "\n\n" +
			"Adjust your review accordingly — e.g. if asked to tone it down or drop nitpicks, " +
			"raise your bar and report only findings that clearly meet it; if asked to focus on " +
			"a specific area, prioritize that. Honor this guidance.\n\n"
	}

	prContextSection := ""
	if o.PrNarrative != "" || len(o.RiskSurfaces) > 0 {
		narrative := o.PrNarrative
		if narrative == "" {
			narrative = "not provided"
		}
		risks := "none provided"
		if len(o.RiskSurfaces) > 0 {
			risks = joinComma(o.RiskSurfaces)
		}
		prContextSection = "## PR Context\n\n" +
			"PR narrative: " + narrative + "\n" +
			"Risk surfaces: " + risks + "\n\n"
	}

	intakeSection := ""
	if o.IntakeSummary != "" {
		intakeSection = "## Intake Summary\n\n" + o.IntakeSummary + "\n\n"
	}

	dimensionsSection := "## Other Review Dimensions\n\n" +
		"Other dimensions being reviewed in parallel: " + joinComma(orEmpty(o.AllDimensionNames)) + ". " +
		"Avoid duplicating findings that clearly belong to another dimension.\n\n"

	diffSection := ""
	if len(o.DiffPatches) > 0 {
		var parts []string
		for _, path := range o.TargetFiles {
			if patch, ok := o.DiffPatches[path]; ok && patch != "" {
				parts = append(parts, "### "+path+"\n```diff\n"+patch+"\n```")
			}
		}
		if len(parts) > 0 {
			patchesText := strings.Join(parts, "\n\n")
			if o.RepoPath != "" && utf8.RuneCountInString(patchesText) > 6000 {
				pf := contextPath(o.RepoPath, ".pr-af-context", "review_dimension_diff_patches.md")
				diffSection = "## Diff Patches for Target Files\n\n" +
					"Full diff patches written to: " + pf + "\n" +
					"Read this file for detailed target-file patches.\n\n"
			} else {
				diffSection = "## Diff Patches for Target Files\n\n" + patchesText + "\n\n"
			}
		}
	}

	primedSection := ""
	if o.PrimedCode != "" {
		if o.RepoPath != "" && utf8.RuneCountInString(o.PrimedCode) > 6000 {
			pf := contextPath(o.RepoPath, ".pr-af-context", "review_dimension_primed_code.md")
			primedSection = "## Target-File Code (pre-read for you)\n\n" +
				"The current content of your target files (with line numbers) and their import " +
				"context is written to: " + pf + "\nRead that file FIRST — it is the code you would " +
				"otherwise navigate to. Open additional files only if it is insufficient.\n\n"
		} else {
			primedSection = "## Target-File Code (pre-read for you)\n\n" +
				"The current content of your target files (with line numbers) and import context " +
				"is below — this is the code you would otherwise navigate to. Reason over it " +
				"directly; open additional files only if it is insufficient.\n\n" +
				o.PrimedCode + "\n\n"
		}
	}

	spawnInstruction := ""
	if canSpawn {
		spawnInstruction = "\n\nSUB-REVIEW SPAWNING: You may request deeper sub-reviews for areas that need " +
			"specialized investigation beyond your current scope. Only request a sub-review when:\n" +
			"- You found a complex issue that requires reading additional files not in your target list\n" +
			"- A finding reveals a pattern that may repeat across other files\n" +
			"- You suspect a security/correctness issue but lack context to confirm it\n" +
			"Current depth: " + strconv.Itoa(o.CurrentDepth) + "/" + strconv.Itoa(o.MaxDepth) + ". " +
			"You have " + strconv.Itoa(o.MaxDepth-o.CurrentDepth) + " level(s) of sub-review remaining. " +
			"Do NOT request sub-reviews for trivial issues or things you can resolve yourself. " +
			"Maximum 2 sub-reviews per dimension."
	} else {
		spawnInstruction = "\n\nYou are at maximum review depth. Do NOT request any sub-reviews. " +
			"Report all findings directly, even if uncertain."
	}

	contextFiles := "none"
	if len(o.ContextFiles) > 0 {
		contextFiles = joinComma(o.ContextFiles)
	}

	return "You are a senior engineer performing a focused code review. You have been assigned " +
		"a specific review dimension with a clear investigation question.\n\n" +
		"## Your Assignment\n\n" +
		o.ReviewPrompt + "\n\n" +
		"**Target files** (read and analyze these): " + joinComma(o.TargetFiles) + "\n" +
		"**Context files** (reference as needed): " + contextFiles + "\n\n" +
		feedbackSection +
		prContextSection +
		intakeSection +
		dimensionsSection +
		diffSection +
		primedSection +
		reviewDimStatic +
		spawnInstruction
}

const reviewDimStatic = "## How to Review\n\n" +
	"You have full repository access. When a pre-read target-file code section is provided " +
	"above, reason over it directly and open additional files only when it is insufficient " +
	"(e.g. to follow a definition or caller it does not contain). When it is not provided, " +
	"READ the actual files — the diff shows WHAT changed, the repo shows the FULL context " +
	"of WHY it matters.\n\n" +
	"Do NOT just scan for surface-level issues. Think deeply about what this code DOES:\n\n" +
	"1. **Read the target files thoroughly.** Understand the control flow, data flow, " +
	"and error paths. Pay attention to what happens at boundaries — function entry/exit, " +
	"exception handlers, early returns, decorator effects.\n\n" +
	"2. **Trace implications.** If a function signature changed, who calls it? " +
	"If a default value changed, where is it consumed? If an import was added or removed, " +
	"what depended on it? When checking callers/consumers of changed code, actually search " +
	"the codebase for references and verify call sites in real files.\n\n" +
	"3. **Check behavioral equivalence.** If code was refactored or a library was swapped, " +
	"does the new version handle ALL the same cases? Edge cases matter: empty inputs, " +
	"None values, concurrent access, error conditions, type mismatches.\n\n" +
	"4. **Verify contracts.** Are return types preserved? Are exception types consistent? " +
	"Do decorators inject parameters that callers might not account for? " +
	"Are there implicit ordering dependencies?\n\n" +
	"5. **Think about what's NOT in the diff.** The most dangerous bugs are in code " +
	"that WASN'T changed but SHOULD have been. If a method's signature changed, " +
	"every caller needs updating. If an enum added a variant, every switch/match " +
	"needs the new case.\n\n" +
	"Before reporting a finding, verify your claim against the actual code. Open the file, " +
	"read the function, and confirm the behavior you are claiming exists.\n\n" +
	"## Severity Calibration\n\n" +
	"Use the FULL severity range. A well-calibrated review has a MIX:\n\n" +
	"- **critical**: Runtime crashes, data corruption, security vulnerabilities, " +
	"silent logic errors that produce wrong results. The code WILL fail in production. " +
	"You must be able to describe the EXACT failure scenario — 'X calls Y with Z, " +
	"which causes W'. Vague concerns are not critical.\n" +
	"- **important**: Missing error handling, validation gaps, API contract violations, " +
	"race conditions under realistic load, performance traps with specific data sizes. " +
	"The code CAN fail under known conditions.\n" +
	"- **suggestion**: Better design patterns, improved abstractions, edge cases worth " +
	"handling, test coverage gaps for specific scenarios. The code works but could be " +
	"more robust.\n" +
	"- **nitpick**: Naming, style, readability, documentation. Truly cosmetic.\n\n" +
	"The `severity` field MUST be EXACTLY one of these four lowercase strings: " +
	"`critical`, `important`, `suggestion`, `nitpick`. Do NOT use `high`, `medium`, " +
	"`low`, `warning`, or any other label.\n\n" +
	"If you're unsure whether something is critical or important, provide your reasoning " +
	"in the `body` field and let the confidence score reflect your uncertainty.\n\n" +
	"## False-Positive Prevention (CRITICAL)\n\n" +
	"Before reporting ANY finding, you MUST pass these three gates:\n\n" +
	"### Gate 1: Reachability Proof\n" +
	"Trace the EXACT call path from a real entry point to the buggy code. " +
	"If you cannot construct a concrete scenario where the bug triggers, " +
	"it is NOT a finding — it is speculation. Ask yourself:\n" +
	"- Can this code path actually be reached in production?\n" +
	"- Are there upstream guards, validators, or type checks that prevent the bad state?\n" +
	"- Is the 'broken' behavior actually intentional (defensive coding, legacy compat)?\n\n" +
	"### Gate 2: Evidence Chain\n" +
	"Every finding MUST have a step-by-step evidence chain in the `evidence` field:\n" +
	"```\n" +
	"Step 1: [Entry point] calls [function] with [specific args]\n" +
	"Step 2: [function] passes [value] to [downstream]\n" +
	"Step 3: [downstream] expects [type/value] but receives [actual]\n" +
	"Step 4: This causes [specific failure mode]\n" +
	"```\n" +
	"If you cannot write this chain, the finding is not well-evidenced enough to report.\n\n" +
	"### Gate 3: Confidence Self-Assessment\n" +
	"Rate your confidence honestly. Only report findings with confidence >= 0.6.\n" +
	"- 0.9-1.0: You traced the full path and verified the failure mode\n" +
	"- 0.7-0.8: Strong evidence but some assumptions about runtime state\n" +
	"- 0.6: Reasonable evidence, worth flagging for human review\n" +
	"- Below 0.6: Do NOT report. You are guessing.\n\n" +
	"**Zero tolerance for speculative findings.** Three well-proven findings are worth " +
	"infinitely more than ten speculative ones. When in doubt, DROP the finding.\n\n" +
	"## Output Quality\n\n" +
	"For each finding, use proper GitHub Markdown:\n" +
	"- **body**: Explain the issue clearly. Use `inline code` for identifiers. " +
	"Use code blocks with language hints for snippets. Bold key terms. " +
	"Explain WHY this is a problem, not just WHAT is wrong.\n" +
	"- **evidence**: Quote the EXACT code or trace the EXACT call path that demonstrates " +
	"the issue. Include function names, parameter bindings, and return values. " +
	"'Step 1: X calls Y with arg=Z. Step 2: Y binds Z to parameter W. Step 3: W.foo() " +
	"fails because Z is a list, not a TLS object.'\n" +
	"- **suggestion**: Describe the fix concisely. What to change, where, and why. " +
	"If there are multiple valid approaches, mention the tradeoffs.\n" +
	"- **file_path**: Full path from the repository root.\n" +
	"- **line_start**: The specific line where the issue manifests. Be precise.\n\n" +
	"Do NOT produce findings you aren't confident about just to fill a quota. " +
	"Three well-evidenced findings are worth more than ten vague ones."
