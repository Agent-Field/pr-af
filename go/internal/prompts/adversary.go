package prompts

import (
	"unicode/utf8"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports adversary_phase from reasoners/harnesses.py.

const adversaryIntro = "You are the adversarial reviewer. Your job is to CHALLENGE every finding and " +
	"determine whether it is real or a false positive. You are skeptical by default.\n\n"

const adversaryHasEvidence = "## Ground-Truth Evidence (CRITICAL)\n\n" +
	"Each finding below includes a `ground_truth` section containing ACTUAL CODE " +
	"extracted programmatically from the repository. This is the REAL code — not the " +
	"reviewer's description of it. Use this as your primary verification source:\n\n" +
	"- `primary_code`: The actual source code around the finding location (with line numbers)\n" +
	"- `caller_snippets`: Real call sites of functions mentioned in the finding\n" +
	"- `diff_hunk`: The actual diff patch for this file\n" +
	"- `import_context`: What this file imports and what imports it\n" +
	"- `related_code`: Code from non-PR files that interact with the finding\n\n" +
	"**VERIFICATION PROTOCOL**: For each finding:\n" +
	"1. Read the reviewer's CLAIM about what the code does\n" +
	"2. Read the `ground_truth.primary_code` to see what the code ACTUALLY does\n" +
	"3. If the claim contradicts the ground truth → CHALLENGE as false positive\n" +
	"4. If the claim matches the ground truth → check caller_snippets to verify " +
	"the failure scenario is reachable\n" +
	"5. You may ALSO browse the repo for additional verification, but the ground " +
	"truth should catch most false positives\n\n"

const adversaryNoEvidence = "## Verification Protocol\n\n" +
	"No ground-truth evidence was extracted for these findings. You MUST read the " +
	"actual repository files yourself to verify each finding. Open the file mentioned, " +
	"read the function, and confirm the behavior the reviewer claims exists.\n\n"

const adversaryMiddle = "## For Each Finding, Determine:\n\n" +
	"1. **Does the ground truth match the claim?** Compare the reviewer's description " +
	"against the actual code in `ground_truth.primary_code`. If the reviewer says " +
	"'function X uses string comparison' but the actual code uses `errors.Is()`, " +
	"that is a false positive — CHALLENGE it immediately.\n\n" +
	"2. **Is the failure scenario reachable?** Check `ground_truth.caller_snippets` " +
	"to see if the described call path actually exists. Are there guards upstream " +
	"that prevent the bad state? Does the calling code handle the condition?\n\n" +
	"3. **Is the severity correct?** A 'critical' finding must have a concrete crash " +
	"or corruption scenario traceable through the ground truth. If the primary code " +
	"shows the issue is handled, downgrade or challenge.\n\n" +
	"4. **Cross-file interactions**: Check `ground_truth.related_code` and " +
	"`ground_truth.import_context` to understand the broader context. A finding " +
	"might look valid in isolation but be prevented by code in another file.\n\n" +
	"5. **Hidden traps**: Did the reviewer find a real issue but miss a WORSE " +
	"version visible in the ground truth code?\n\n" +
	"## Verdicts\n\n" +
	"- **confirmed**: The ground truth supports the finding. The claim matches the " +
	"actual code. The failure scenario is reachable.\n" +
	"- **challenged**: The ground truth contradicts the finding. The actual code " +
	"does NOT do what the reviewer claims, OR upstream guards prevent the failure.\n" +
	"- **escalated**: The ground truth reveals the issue is WORSE than the reviewer " +
	"described.\n\n"

// AdversaryPrompt ports adversary_phase. Skepticism escalates to "high" when the
// AI-generated confidence exceeds 0.5; the extra skepticism line appears on the
// same condition. has_evidence is bool(evidenceMap).
func AdversaryPrompt(findings []schemas.ReviewFinding, aiGeneratedConfidence float64, prContext, repoPath string, evidenceMap map[string]*OMap) string {
	skepticism := "standard"
	if aiGeneratedConfidence > 0.5 {
		skepticism = "high"
	}

	withEvidence := make([]*OMap, 0, 20)
	for _, f := range firstN(findings, 20) {
		entry := omap(
			"title", f.Title,
			"severity", string(f.Severity),
			"file_path", f.FilePath,
			"dimension_name", f.DimensionName,
			"body", f.Body,
			"evidence", f.Evidence,
			"suggestion", f.Suggestion,
			"confidence", f.Confidence,
		)
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("ground_truth", omap(
				"primary_code", runeSlice(ev.GetStr("primary_code", ""), 3000),
				"caller_snippets", firstN(ev.GetStrSlice("caller_snippets"), 5),
				"diff_hunk", runeSlice(ev.GetStr("diff_hunk", ""), 2000),
				"import_context", ev.GetStr("import_context", ""),
				"related_code", runeSlice(ev.GetStr("related_code", ""), 2000),
			))
		}
		withEvidence = append(withEvidence, entry)
	}
	summary := pyJSON(withEvidence)

	var findingsRef string
	if utf8.RuneCountInString(summary) > 10000 && repoPath != "" {
		fp := contextPath(repoPath, ".pr-af-context", "adversary_findings.json")
		findingsRef = "Full findings with ground-truth evidence written to: " + fp + "\n" +
			"Read this file for complete finding details and code evidence."
	} else {
		findingsRef = "Findings with ground-truth evidence:\n" + summary
	}

	evidenceInstruction := adversaryNoEvidence
	if len(evidenceMap) > 0 {
		evidenceInstruction = adversaryHasEvidence
	}

	skepLine := "\n"
	if aiGeneratedConfidence > 0.5 {
		skepLine = "(Higher AI confidence: be MORE skeptical of trivial findings)\n\n"
	}
	prBlock := ""
	if prContext != "" {
		prBlock = "## PR Context\n\n" + prContext + "\n\n"
	}

	return adversaryIntro + evidenceInstruction + adversaryMiddle +
		"Skepticism mode: " + skepticism + "\n" +
		"AI-generated confidence: " + pyFloat(aiGeneratedConfidence) + "\n" +
		skepLine + prBlock + findingsRef
}
