// Command mockcli is a deterministic, zero-LLM stand-in for the `opencode` CLI
// used by the PR-AF Go port's harness. PR-AF's default provider is opencode (NOT
// claude), so this binary is built literally named `opencode` and placed ahead of
// the real one on PATH (or pointed at via PR_AF_OPENCODE_BIN) so every
// harnessx.Run subprocess resolves to the mock.
//
// It speaks exactly the AgentField opencode provider protocol
// (sdk/go/harness/opencode.go + schema.go):
//
//   - argv: opencode run --format json [--dir D] [-m MODEL] "<PROMPT>"
//     The PROMPT is a single positional argument (the LAST argv element). PR-AF
//     reasoners set no system prompt, so PROMPT is the rendered reasoner prompt
//     plus the harness's "CRITICAL OUTPUT REQUIREMENTS" suffix.
//   - the mock detects the reasoner role by substring-matching PROMPT against the
//     verbatim reasoner prompts in internal/prompts, dispatches to a per-role
//     builder that returns a REAL typed struct matching the reasoner's harness
//     result schema, extracts the .agentfield_output.json path from PROMPT, and
//     writes the marshaled struct there.
//   - stdout carries the opencode JSON-stream events (a final `result` event);
//     exit 0.
//
// IMPORTANT: the mock only ever sees the opencode HARNESS reasoners. PR-AF's four
// .ai() touchpoints (intake gate, coverage gate, merge-gate, polish) use
// agent.AI() -> OpenRouter and never spawn this binary (see test/e2e/run.sh).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// roleMatch maps a distinctive substring of a reasoner prompt to a role id. Every
// substring is the verbatim opening (or a unique line) of one reasoner's prompt,
// lifted from internal/prompts/testdata. Order is not significant — the
// substrings are mutually exclusive — but the most distinctive appear first.
var roleMatchers = []struct{ substr, role string }{
	{"designing review dimensions through the SEMANTIC lens", "meta_semantic"},
	{"designing review dimensions through the MECHANICAL lens", "meta_mechanical"},
	{"designing review dimensions through the SYSTEMIC lens", "meta_systemic"},
	{"senior engineer performing structural analysis of a pull request", "anatomy"},
	{"You are the adversarial reviewer", "adversary"},
	{"independent verification of code review findings before they reach the adversarial", "evidence_verifier"},
	{"You are a compound-risk investigator for PR findings", "compound_finder"},
	{"You are a deduplication specialist reviewing compound findings", "compound_dedup"},
	{"deciding which of an AI reviewer's findings to actually POST", "post_worthiness"},
	{"You are the LITERAL-CORRECTNESS verifier on a review", "deepen"},
	{"You map the CONSISTENCY OBLIGATIONS the changed code creates", "extract_obligations"},
	{"You verify ONE consistency obligation, and nothing else", "verify_obligation"},
	{"Classify this pull request for a multi-agent review pipeline", "intake_fallback"},
	{"You are a principal engineer designing a review strategy for a pull request", "planning"},
	// review_dimension: the scaffolding line matches both the normal reviewer and
	// the coverage-gap variant (whose gap text is embedded as the assignment).
	{"You are a senior engineer performing a focused code review", "review_dimension"},
	{"Coverage gap review — this area was missed", "review_dimension"},
}

func detectRole(prompt string) string {
	// The runner's schema-retry followups (BuildFollowupPrompt /
	// BuildIncrementalFollowup, sdk harness/schema.go) are sent STANDALONE — they
	// do not contain the original reasoner prompt — so they can never match a
	// role. Detect them explicitly: the mock must NOT treat a retry as an unknown
	// fresh call and overwrite the (possibly good) output file with {}. Exactly
	// that clobbering produced the 0-finding review in e2e run 20260710-154635.
	if strings.Contains(prompt, "PREVIOUS ATTEMPT FAILED") ||
		strings.Contains(prompt, "PARTIAL OUTPUT NEEDS FIXES") {
		return "retry"
	}
	for _, m := range roleMatchers {
		if strings.Contains(prompt, m.substr) {
			return m.role
		}
	}
	return "unknown"
}

func main() {
	// -dump-scenario prints the baked-in default scenario as JSON (used by the
	// runner to materialize PR_AF_MOCK_SCENARIO in sync with the mock).
	if len(os.Args) == 2 && os.Args[1] == "-dump-scenario" {
		b, _ := json.MarshalIndent(defaultScenario(), "", "  ")
		fmt.Println(string(b))
		return
	}

	prompt := promptFromArgs(os.Args[1:])
	sc := loadScenario()
	role := detectRole(prompt)

	// Every invocation logs the prompt head (and, for retries, the runner's
	// embedded Error diagnosis) so a failing run is diagnosable from
	// invocations.jsonl alone.
	extra := map[string]any{"head": promptHead(prompt, 120)}

	switch role {
	case "retry":
		// A schema-retry followup. Whatever is (or isn't) in the output file is
		// the previous attempt's state — leave it untouched. Overwriting here is
		// what destroyed the good review_dimension output in the failing run.
		extra["diagnosis"] = retryErrorFrom(prompt)
	case "unknown":
		// Do not write anything: a missing output file makes the runner report
		// "The output file was NOT created" and eventually seed defaults, which
		// is strictly safer than clobbering a shared output path with {}.
		fmt.Fprintf(os.Stderr, "mockcli: unknown role for prompt head %q\n", promptHead(prompt, 120))
	default:
		value := dispatch(role, prompt, sc)
		outPath := outputPathFrom(prompt)
		if outPath != "" {
			if b, err := json.Marshal(value); err == nil {
				if err := os.WriteFile(outPath, b, 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "mockcli: write %s (role %s): %v\n", outPath, role, err)
					extra["write_error"] = err.Error()
				}
			} else {
				fmt.Fprintf(os.Stderr, "mockcli: marshal role %s: %v\n", role, err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "mockcli: no output path found for role %s\n", role)
			extra["write_error"] = "no output path in prompt"
		}
	}

	logInvocation(role, extra)

	// Emit the opencode JSON-stream result event the provider parses
	// (extractOpenCodeFinalText handles `type:"result"`), then exit 0.
	n := nextCount("invocations|total")
	result := map[string]any{
		"type":   "result",
		"result": fmt.Sprintf("mock ok (%s) [#%d]", role, n),
	}
	line, _ := json.Marshal(result)
	fmt.Println(string(line))
}

// dispatch routes a detected role to its output builder.
func dispatch(role, prompt string, sc Scenario) any {
	switch role {
	case "anatomy":
		return roleAnatomy(prompt)
	case "meta_semantic":
		return roleMeta(prompt, "semantic")
	case "meta_mechanical":
		return roleMeta(prompt, "mechanical")
	case "meta_systemic":
		return roleMeta(prompt, "systemic")
	case "review_dimension":
		return roleReviewDimension(prompt, sc)
	case "evidence_verifier":
		return roleEvidenceVerifier(prompt)
	case "adversary":
		return roleAdversary(prompt, sc)
	case "compound_finder":
		return roleCompoundFinder()
	case "compound_dedup":
		return roleKeepAll(prompt, "Mock dedup: keep all compound findings.")
	case "post_worthiness":
		return roleKeepAll(prompt, "Mock post-worthiness: keep all findings.")
	case "deepen":
		return roleDeepen()
	case "extract_obligations":
		return roleObligations()
	case "verify_obligation":
		return roleVerifyObligation()
	case "intake_fallback":
		return roleIntakeFallback(prompt)
	case "planning":
		return rolePlanning()
	default:
		// Unreachable from main (retry/unknown are handled before dispatch);
		// defensive empty object for any future call site.
		return map[string]any{}
	}
}
