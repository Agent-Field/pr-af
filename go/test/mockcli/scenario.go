package main

import (
	"encoding/json"
	"os"
)

// scenario.go holds the deterministic knobs the mock roles read. Unlike SWE-AF's
// large multi-issue coding scenario, PR-AF's review pipeline is stateless per
// call, so the scenario is small: how many findings each review dimension emits,
// their severities, and which adversary verdict to hand back. A baked default is
// used unless PR_AF_MOCK_SCENARIO points at a JSON override (materialized by the
// e2e run.sh via -dump-scenario so the file and the mock stay in sync).

// Scenario is the mock's deterministic behavior spec.
type Scenario struct {
	// ReviewFinding severities emitted by each review_dimension call. Two entries
	// => "a couple of findings" per dimension (design/T4.3 spec). The first
	// finding is anchored to the dimension's first target file at LineStart so the
	// orchestrator's comment-eligibility gate (line in a diff hunk) can pass on a
	// simple single-hunk fixture diff.
	ReviewFindingSeverities []string `json:"review_finding_severities"`

	// LineStart is the line the first finding of every dimension points at. The
	// e2e fixture seeds an added block starting at line 1, so LineStart=1 keeps the
	// finding inside a diff hunk.
	LineStart int `json:"line_start"`

	// Confidence for emitted findings. >=0.6 clears the review_dimension prompt's
	// self-assessment bar; >0 keeps them past scoring's confidence-threshold drop.
	Confidence float64 `json:"confidence"`

	// AdversaryVerdict is applied to the FIRST challenged finding ("adversary
	// confirms one"); every other finding is "confirmed" too so nothing is dropped,
	// but exactly one carries AdversaryConfirmReason to make the confirmation
	// visible in the log/DAG.
	AdversaryVerdict string `json:"adversary_verdict"`
}

// defaultScenario is the baked-in behavior: two findings per dimension (one
// important, one suggestion), anchored at line 1, adversary-confirmed.
func defaultScenario() Scenario {
	return Scenario{
		ReviewFindingSeverities: []string{"important", "suggestion"},
		LineStart:               1,
		Confidence:              0.8,
		AdversaryVerdict:        "confirmed",
	}
}

// loadScenario returns the PR_AF_MOCK_SCENARIO override when set and parseable,
// else the baked default. Unknown/missing fields fall back to the default value.
func loadScenario() Scenario {
	sc := defaultScenario()
	path := os.Getenv("PR_AF_MOCK_SCENARIO")
	if path == "" {
		return sc
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sc
	}
	// Decode over the default so absent keys keep their default value.
	_ = json.Unmarshal(b, &sc)
	if len(sc.ReviewFindingSeverities) == 0 {
		sc.ReviewFindingSeverities = defaultScenario().ReviewFindingSeverities
	}
	if sc.AdversaryVerdict == "" {
		sc.AdversaryVerdict = defaultScenario().AdversaryVerdict
	}
	return sc
}
