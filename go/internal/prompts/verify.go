package prompts

import (
	"path/filepath"
	"unicode/utf8"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports evidence_verifier from reasoners/harnesses.py.

const evidenceVerifierPreamble = "You are a senior engineer performing independent verification of code review findings " +
	"before they reach the adversarial challenge phase. Each finding below was produced by " +
	"a reviewer who read the repository, and each includes `extracted_code` — real source " +
	"code pulled programmatically from the repo around the finding location.\n\n" +
	"## Your Role\n\n" +
	"You are not the original reviewer, and you are not the adversary. You are an " +
	"independent investigator. Your job is to determine what the code ACTUALLY does " +
	"at each finding location, and whether the reviewer's claim about the code's " +
	"behavior is factually accurate.\n\n" +
	"## How to Investigate\n\n" +
	"For each finding, you have two sources of truth:\n\n" +
	"1. **`extracted_code`** — actual source code around the finding location, call sites " +
	"of mentioned functions, the diff patch, and import/dependency context. This was " +
	"extracted programmatically, so it is what the code really says.\n\n" +
	"2. **The repository itself** — you have full access. Use it to trace connections " +
	"the extracted code doesn't cover: follow function calls across modules, check how " +
	"values flow through layers, understand the broader architecture around the finding.\n\n" +
	"Start with the extracted code to understand the local picture. Then browse the repo " +
	"to understand the broader context — how does this code connect to the rest of the " +
	"system? What are the upstream callers and downstream consumers? What are the implicit " +
	"contracts this code participates in?\n\n" +
	"## What to Determine\n\n" +
	"For each finding, answer these questions through investigation:\n\n" +
	"- **Does the code actually behave as the reviewer claims?** Read the `extracted_code` " +
	"and compare it against the reviewer's description in `body`. If the reviewer says " +
	"'this function uses string comparison' but the extracted code shows `errors.Is()`, " +
	"the claim is factually wrong.\n\n" +
	"- **Is the described scenario actually reachable?** Check `caller_snippets` and " +
	"browse the repo for call paths. Can the problematic state the reviewer describes " +
	"actually occur in practice? Are there guards, validators, or type constraints " +
	"upstream that prevent it?\n\n" +
	"- **What does the broader context reveal?** The `import_context` and `related_code` " +
	"show how this file connects to the rest of the codebase. Sometimes a finding looks " +
	"valid in isolation but is prevented by code in another module. Sometimes it looks " +
	"minor in isolation but is amplified by how the code is used elsewhere.\n\n" +
	"- **Is the severity proportionate?** Based on what you found, does the severity " +
	"match the actual impact? A 'critical' finding should have a concrete, traceable " +
	"failure path. An 'important' finding should have a realistic scenario.\n\n" +
	"## Output\n\n" +
	"For each finding, return:\n" +
	"- `title`: the finding's title (must match exactly)\n" +
	"- `verified`: true if the code behavior matches the reviewer's claim, false if it doesn't\n" +
	"- `actual_behavior`: what the code ACTUALLY does at this location (brief, factual)\n" +
	"- `revised_severity`: your assessment of the correct severity (critical/important/suggestion/nitpick)\n" +
	"- `revised_confidence`: your confidence in the finding's validity (0.0-1.0)\n" +
	"- `verification_notes`: what you found during investigation that the downstream " +
	"adversary should know — especially any discrepancies between the claim and reality, " +
	"or important context from the broader codebase\n\n"

// EvidenceVerifierPrompt ports evidence_verifier. evidenceMap is keyed by
// finding title.
func EvidenceVerifierPrompt(findings []schemas.ReviewFinding, evidenceMap map[string]*OMap, prContext, repoPath string) string {
	payload := make([]*OMap, 0, len(findings))
	for _, f := range findings {
		entry := omap(
			"title", f.Title,
			"severity", string(f.Severity),
			"file_path", f.FilePath,
			"line_start", f.LineStart,
			"dimension_name", f.DimensionName,
			"body", f.Body,
			"evidence", f.Evidence,
			"confidence", f.Confidence,
		)
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("extracted_code", omap(
				"primary_code", runeSlice(ev.GetStr("primary_code", ""), 4000),
				"caller_snippets", firstN(ev.GetStrSlice("caller_snippets"), 5),
				"diff_hunk", runeSlice(ev.GetStr("diff_hunk", ""), 2000),
				"import_context", ev.GetStr("import_context", ""),
				"related_code", runeSlice(ev.GetStr("related_code", ""), 2000),
				"cross_ref_snippets", firstN(ev.GetStrSlice("cross_ref_snippets"), 3),
			))
		}
		payload = append(payload, entry)
	}
	findingsText := pyJSON(payload)

	var findingsRef string
	if utf8.RuneCountInString(findingsText) > 12000 && repoPath != "" {
		fp := filepath.Join(repoPath, ".pr-af-context", "verification_findings.json")
		findingsRef = "Findings with extracted code written to: " + fp + "\n" +
			"Read this file for the full list of findings and their extracted code context."
	} else {
		findingsRef = findingsText
	}

	prBlock := ""
	if prContext != "" {
		prBlock = "## PR Context\n\n" + prContext + "\n\n"
	}
	return evidenceVerifierPreamble + prBlock + "## Findings to Verify\n\n" + findingsRef
}
