package prompts

import (
	"strconv"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports the merge-gate prompt (merge_gate.py), the polish prompt (polish.py),
// and the coverage-gap review prompt (orchestrator._build_gap_dimensions).

// MergeGateSystem ports merge_gate._MERGE_GATE_SYSTEM.
const MergeGateSystem = "You are the release manager for an automated code reviewer. Your job is to " +
	"decide whether a single review finding must be fixed BEFORE this PR is merged, " +
	"or whether the team can safely merge now and address it later.\n" +
	"\n" +
	"Apply a TIGHT bar. Only call something `blocking` if at least one is true:\n" +
	"  - It breaks the build, tests, or type-checking.\n" +
	"  - It introduces a security vulnerability reachable from a real user-facing " +
	"code path (auth bypass, injection, credential leak, RCE, exposed secret, " +
	"missing access control on a route real clients hit).\n" +
	"  - It causes data loss, data corruption, or irreversible state damage in " +
	"production-running code.\n" +
	"  - It breaks an existing public API/CLI/schema contract that real callers " +
	"depend on, with no migration path.\n" +
	"  - It is a regression of behavior that was working before this PR.\n" +
	"\n" +
	"Treat the following as NON-blocking (return blocking=false):\n" +
	"  - Code quality, style, naming, refactor opportunities.\n" +
	"  - Missing tests for edge cases, low test coverage, mock signature drift in " +
	"test helpers.\n" +
	"  - Defensive programming opportunities, missing input validation that has " +
	"no demonstrated reachable exploit path.\n" +
	"  - Performance suggestions that don't change correctness.\n" +
	"  - Documentation, comments, README, type-hint completeness.\n" +
	"  - 'Should also handle X' suggestions when X isn't currently reachable.\n" +
	"  - Architectural critiques (DRY, single source of truth, layering) without a " +
	"concrete production impact described in the finding.\n" +
	"  - Issues whose reachability or exploitability the finding itself cannot " +
	"demonstrate concretely.\n" +
	"\n" +
	"If the finding's evidence does NOT concretely demonstrate one of the blocking " +
	"criteria above — even when the severity is labeled 'critical' — return " +
	"blocking=false. Reviewers are often alarmist; you are the calibrating layer.\n" +
	"\n" +
	"Output strict JSON with this exact shape and nothing else:\n" +
	`  {"blocking": true | false, "reason": "<one short sentence>"}` + "\n" +
	"Do not add prose. Do not wrap in markdown fences. JSON only."

// MergeGateUserPrompt ports merge_gate._build_user_prompt: the per-finding user
// prompt. Evidence and Suggested-fix sections are included only when truthy
// (Python `if finding.evidence` / `if finding.suggestion`).
func MergeGateUserPrompt(f schemas.ScoredFinding) string {
	s := "# Finding\n" +
		"Severity (reviewer's label): " + string(f.Severity) + "\n" +
		"Confidence: " + pyDotTwoF(f.Confidence) + "\n" +
		"File: " + f.FilePath + ":" + strconv.Itoa(f.LineStart) + "\n" +
		"Title: " + f.Title + "\n" +
		"\n## Body\n" + f.Body + "\n"
	if f.Evidence != "" {
		s += "\n## Evidence\n" + f.Evidence + "\n"
	}
	if f.Suggestion != nil && *f.Suggestion != "" {
		s += "\n## Suggested fix\n" + *f.Suggestion + "\n"
	}
	s += "\n# Question\n" +
		"Must this be fixed before this PR is merged to production? " +
		"Apply the bar described in the system prompt. Reply with JSON only."
	return s
}

// PolishSystem ports polish._POLISH_SYSTEM.
const PolishSystem = "You rewrite GitHub PR review comments. A good PR comment tells the author " +
	"exactly what to fix and why, so they can act in under 30 seconds. Open with a " +
	"one-sentence directive. Then one short paragraph (2-3 sentences) on the concrete " +
	"failure mode — no abstract security lectures, no 'attacker-controlled' filler. " +
	"Preserve every file path, line number, identifier, code block, markdown " +
	"header, GitHub alert callout (`> [!CAUTION]`, `> [!NOTE]`), `<details>` block, " +
	"and `<sub>` line verbatim. Never invent facts. Never soften severity. Output " +
	"the polished comment body only — no preamble, no commentary."

// PolishUserPrompt ports polish._polish_one's user prompt.
func PolishUserPrompt(body string) string {
	return "Rewrite this PR review comment to be concise and developer-focused.\n\n" + body
}

// CoverageGapPrompt ports orchestrator._build_gap_dimensions' review_prompt.
func CoverageGapPrompt(gap string) string {
	return "Coverage gap review — this area was missed in the initial review pass.\n\n" +
		"Gap identified: " + gap + "\n\n" +
		"Inspect the target files with the same depth and rigor as a primary review. " +
		"Look for bugs, logic errors, security issues, and behavioral changes. " +
		"Pay special attention to how this code interacts with the changes that were " +
		"already reviewed in other files — the gap exists because this cluster's " +
		"relationship to the main change wasn't obvious at planning time."
}
