package prompts

// Ports the intake_phase builders from reasoners/harnesses.py:
//   - the .ai() gate system + user prompt (metadata classification), and
//   - the .harness() fallback prompt (full classification).

// IntakeGateSystem is the system prompt for the intake .ai() gate.
const IntakeGateSystem = "Return pr_type, complexity, and confident only. Use the provided schema."

// IntakeGatePrompt builds the intake .ai() gate user prompt. languages must be
// pre-sorted (the reasoner uses sorted(_extract_languages(pr))); commitMessages
// is truncated to the first 5 and description to the first 4000 runes, matching
// the Python json payload.
func IntakeGatePrompt(title, description string, labels []string, author string, filesChanged int, languages, commitMessages []string) string {
	ctx := omap(
		"title", title,
		"description", delimitPRDescription(runeSlice(description, 4000)),
		"labels", orEmpty(labels),
		"author", author,
		"files_changed", filesChanged,
		"languages", orEmpty(languages),
		"commit_messages", orEmpty(firstN(commitMessages, 5)),
	)
	return "Classify this pull request from metadata and diff footprint.\n\n" + pyJSON(ctx)
}

// IntakeFallbackPrompt builds the intake_phase .harness() fallback prompt.
// description is truncated to the first 4000 runes.
func IntakeFallbackPrompt(title, description, requestedDepth string, languages []string, filesChanged int) string {
	ctx := omap(
		"pr_title", title,
		"description", delimitPRDescription(runeSlice(description, 4000)),
		"requested_depth", requestedDepth,
		"languages", orEmpty(languages),
		"files_changed", filesChanged,
	)
	return "Classify this pull request for a multi-agent review pipeline. " +
		"Downstream reviewers will rely on your classification to decide review depth " +
		"and focus areas, so accuracy matters more than speed.\n\n" +
		"Determine: PR type (feature/bugfix/refactor/docs/config/dependency/test), " +
		"complexity (trivial/standard/complex/massive), areas touched, risk signals, " +
		"AI-generation confidence, and write a technical PR summary that captures the " +
		"actual substance of the change (not just the PR title restated).\n\n" + pyJSON(ctx)
}
