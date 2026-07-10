package prompts

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Ports deepen_findings from reasoners/harnesses.py, plus the diff-patch
// rendering helpers shared with extract_obligations.

// filterPatches ports `{k: v for k, v in (diff_patches or {}).items() if v}`:
// drops empty-valued patches, dedups keys (last value wins, first position).
func filterPatches(patches []StrPair) []StrPair {
	var order []string
	idx := map[string]int{}
	out := []StrPair{}
	for _, p := range patches {
		if p.Val == "" {
			continue
		}
		if i, ok := idx[p.Key]; ok {
			out[i].Val = p.Val
			continue
		}
		idx[p.Key] = len(out)
		order = append(order, p.Key)
		out = append(out, p)
	}
	_ = order
	return out
}

// renderPatchesText ports "\n\n".join(f"### {p}\n```diff\n{d}\n```" ...) over
// the first 20 (already-filtered) patches.
func renderPatchesText(patches []StrPair) string {
	patches = firstN(patches, 20)
	parts := make([]string, len(patches))
	for i, p := range patches {
		parts[i] = "### " + p.Key + "\n```diff\n" + p.Val + "\n```"
	}
	return strings.Join(parts, "\n\n")
}

// diffRef ports the shared diff_ref resolution: inline unless a repo path is set
// and the rendered patches exceed 9000 characters, in which case they are
// referenced by file path. fileName is "deepen_diff.md" / "obligations_diff.md".
func diffRef(patches []StrPair, repoPath, fileName string) string {
	text := renderPatchesText(patches)
	if utf8.RuneCountInString(text) > 9000 && repoPath != "" {
		fp := filepath.Join(repoPath, ".pr-af-context", fileName)
		return "Changed-code diffs written to: " + fp + "\nRead it for the full set of hunks."
	}
	return "## Changed code (diffs)\n\n" + text
}

const deepenPre1 = "You are the LITERAL-CORRECTNESS verifier on a review. Other reviewers have already " +
	"covered architecture, design, tests, and systemic concerns. Your job is the opposite " +
	"and complementary one: go line by line through the CHANGED code and verify it is " +
	"literally correct against GROUND TRUTH — the real definitions of every symbol it " +
	"touches. You have full repository access; USE it to resolve definitions, do not guess.\n\n" +
	"## The single discipline\n" +
	"For each changed line, identify every external thing the code DEPENDS ON and RELIES ON " +
	"being true, then open the actual definition and verify the assumption holds. When the " +
	"ground truth contradicts the code's assumption, that is a finding.\n\n" +
	"Be EXHAUSTIVE, not selective. Walk EVERY changed call, argument, assignment, condition, " +
	"and type assumption — one at a time. Emit a finding for EVERY violation you confirm. " +
	"Stopping after the single most salient issue is a FAILURE of this pass; completeness is " +
	"the goal. Two sibling bugs on adjacent lines are TWO findings, not one.\n\n" +
	"Let the specific things to check EMERGE from this code — do NOT work from a remembered " +
	"list of common bug types or categories. For each changed element, ask the question the " +
	"code itself raises: 'what must be true — elsewhere in this codebase, or in the runtime — " +
	"for this exact line to be correct?' Then read the real definition, caller, creation site, " +
	"or surrounding logic and check whether it actually is true. The right questions are " +
	"different for every change; derive them from what the code does, never from memory of " +
	"past bugs. Follow an assumption across files when the answer lives elsewhere — a value " +
	"produced in one place and relied on in another must agree. When the ground truth " +
	"contradicts what the code assumes, that is a finding — whether the violation is mechanical " +
	"(a symbol that does not resolve or behave as used) or a logic invariant the code is " +
	"meant to preserve but does not. State the concrete consequence you can demonstrate.\n\n" +
	"## Output contract\n" +
	"- Findings already reported by other reviewers (do NOT duplicate): "

const deepenPre2 = ".\n" +
	"- Emit a finding only for a concrete, code-verified literal-correctness violation. Each: " +
	"title, severity, file_path, line_start, line_end, body, evidence (quote the exact code " +
	"AND the conflicting definition you read), suggestion, confidence, tags. severity MUST be " +
	"one of critical/important/suggestion/nitpick.\n" +
	"- Verify every claim by reading the real definition. Do NOT speculate or invent. " +
	"confidence >= 0.6. If the changed code is literally correct, return zero findings.\n\n"

// DeepenFindingsPrompt ports deepen_findings. Caller invokes only when there is
// at least one non-empty patch. existingTitles is truncated to 40 and joined
// with "; " (empty -> "none").
func DeepenFindingsPrompt(diffPatches []StrPair, existingTitles []string, repoPath, prContext string) string {
	seen := strings.Join(firstN(existingTitles, 40), "; ")
	if seen == "" {
		seen = "none"
	}
	prBlock := ""
	if prContext != "" {
		prBlock = "## PR Context\n\n" + prContext + "\n\n"
	}
	return deepenPre1 + seen + deepenPre2 + prBlock + diffRef(filterPatches(diffPatches), repoPath, "deepen_diff.md")
}
