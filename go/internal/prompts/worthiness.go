package prompts

import (
	"strconv"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports post_worthiness_gate's prompt from reasoners/harnesses.py. The caller
// only invokes this when len(findings) > 1 (Python early-returns otherwise).
func PostWorthinessPrompt(findings []schemas.ScoredFinding) string {
	lines := make([]string, len(findings))
	for i, f := range findings {
		lines[i] = "[" + strconv.Itoa(i) + "] (" + string(f.Severity) + ") " +
			f.FilePath + ":" + strconv.Itoa(f.LineStart) + " " + f.Title + "\n" +
			"    body: " + runeSlice(f.Body, 300) + "\n    evidence: " + runeSlice(f.Evidence, 180)
	}
	numbered := strings.Join(lines, "\n")
	return "You are an experienced engineer deciding which of an AI reviewer's findings to actually " +
		"POST as comments on a pull request. KEEP every finding that is a genuine, concrete, correct " +
		"defect with clear evidence — a real bug, security, data, or correctness problem. There is NO " +
		"limit: keep as many as are genuinely real. DROP only (a) nitpicks/style/naming/doc/" +
		"test-coverage observations and (b) findings whose evidence does not concretely demonstrate a " +
		"real problem (speculative, unverifiable, already-handled). When genuinely unsure whether " +
		"something is a real bug, KEEP it — favor catching the bug over silence. Judge each on its " +
		"own evidence; do NOT work from a list of bug types.\n\n" +
		"FINDINGS (" + strconv.Itoa(len(findings)) + "):\n\n" + numbered + "\n\n" +
		"Return `keep_indices` (0-based) for the findings worth posting, and brief reasoning."
}
