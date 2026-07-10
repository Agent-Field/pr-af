package config

import "strings"

// BudgetConfig ports config.py BudgetConfig — global and per-phase budget caps.
// Numeric defaults are the §D "Config numeric tables" verbatim. Note
// max_duration_seconds defaults to 1800 here but is always overridden per call
// to the resolved value (300 by default) via ReviewConfig.FromInput.
type BudgetConfig struct {
	MaxCostUSD         float64            `json:"max_cost_usd"`
	MaxDurationSeconds int                `json:"max_duration_seconds"`
	PhaseBudgets       map[string]float64 `json:"phase_budgets"`

	MaxConcurrentReviewers int `json:"max_concurrent_reviewers"`

	MaxReferenceFollowsPerReviewer int `json:"max_reference_follows_per_reviewer"`
	MaxChildSpawnsPerReviewer      int `json:"max_child_spawns_per_reviewer"`

	MaxCrossRefDeepDives int `json:"max_cross_ref_deep_dives"`

	MaxCoverageIterations int `json:"max_coverage_iterations"`

	MaxReviewDepth int `json:"max_review_depth"`

	// EvidencePackReviewers pre-reads each dimension's target files and injects
	// them so reviewers reason over a primed pack. Default ON: env
	// PR_AF_EVIDENCE_PACK not in {"0","false","no"}.
	EvidencePackReviewers bool `json:"evidence_pack_reviewers"`
}

// DefaultPhaseBudgets is the per-phase USD allocation (config.py). Returned as a
// fresh map per call so callers cannot mutate a shared instance.
func DefaultPhaseBudgets() map[string]float64 {
	return map[string]float64{
		"intake":         0.05,
		"anatomy":        0.15,
		"meta_selectors": 0.30, // 3 parallel lenses
		"review":         0.90, // Most budget goes here
		"adversary":      0.40, // Parallel batches
		"cross_ref":      0.30,
		"coverage":       0.10,
		"synthesis":      0.00, // Code, no LLM cost
		"output":         0.00, // Code, no LLM cost
	}
}

// DefaultBudgetConfig builds a BudgetConfig with the config.py defaults, reading
// PR_AF_EVIDENCE_PACK at call time.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxCostUSD:                     2.0,
		MaxDurationSeconds:             1800,
		PhaseBudgets:                   DefaultPhaseBudgets(),
		MaxConcurrentReviewers:         8,
		MaxReferenceFollowsPerReviewer: 3,
		MaxChildSpawnsPerReviewer:      2,
		MaxCrossRefDeepDives:           5,
		MaxCoverageIterations:          2,
		MaxReviewDepth:                 2,
		EvidencePackReviewers:          evidencePackDefault(),
	}
}

// evidencePackDefault ports the PR_AF_EVIDENCE_PACK default_factory (default ON,
// disabled only by an explicit "0"/"false"/"no").
func evidencePackDefault() bool {
	switch strings.ToLower(strEnv("PR_AF_EVIDENCE_PACK", "1")) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// ResolveBudgetCaps ports app.py:62-77 _resolve_budget_caps. An explicit
// argument always wins; when nil, the PR_AF_MAX_COST_USD / PR_AF_MAX_DURATION_
// SECONDS env vars are consulted; finally the historical defaults 2.0 / 300.
// A malformed env value is an error (Python's float()/int() raises inside
// review(), where the ValueError class maps to HTTP 400).
func ResolveBudgetCaps(maxCostUSD *float64, maxDurationSeconds *int) (float64, int, error) {
	cost := 2.0
	if maxCostUSD != nil {
		cost = *maxCostUSD
	} else {
		var err error
		if cost, err = floatEnv("PR_AF_MAX_COST_USD", 2.0); err != nil {
			return 0, 0, err
		}
	}

	dur := 300
	if maxDurationSeconds != nil {
		dur = *maxDurationSeconds
	} else {
		var err error
		if dur, err = intEnv("PR_AF_MAX_DURATION_SECONDS", 300); err != nil {
			return 0, 0, err
		}
	}
	return cost, dur, nil
}
