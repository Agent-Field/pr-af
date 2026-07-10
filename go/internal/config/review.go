package config

import (
	"sort"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// ModelConfig ports config.py ModelConfig — model routing per agent. Budget
// models for gates/classification; premium models for planning/reviewing/
// challenging (plan quality = review quality, so the planner gets premium).
type ModelConfig struct {
	IntakeGate      string `json:"intake_gate"`      // .ai() fast classification
	IntakeFallback  string `json:"intake_fallback"`  // .harness() when not confident
	AnatomySemantic string `json:"anatomy_semantic"` // Narrative understanding
	Planner         string `json:"planner"`          // THE critical agent
	Reviewer        string `json:"reviewer"`         // Deep code analysis
	CrossRef        string `json:"cross_ref"`        // Interaction detection
	Adversary       string `json:"adversary"`        // Challenge quality
	CoverageGate    string `json:"coverage_gate"`    // Simple completeness check
	DedupGate       string `json:"dedup_gate"`       // Near-duplicate detection
}

// DefaultModelConfig builds the config.py model-routing defaults.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		IntakeGate:      "budget",
		IntakeFallback:  "mid",
		AnatomySemantic: "mid",
		Planner:         "premium",
		Reviewer:        "premium",
		CrossRef:        "premium",
		Adversary:       "premium",
		CoverageGate:    "budget",
		DedupGate:       "budget",
	}
}

// setByJSONName sets the field identified by its snake_case json name, mirroring
// Python's `setattr(config.models, field_name, model_id) if hasattr(...)`.
// Unknown names are ignored (returns false).
func (m *ModelConfig) setByJSONName(name, value string) bool {
	switch name {
	case "intake_gate":
		m.IntakeGate = value
	case "intake_fallback":
		m.IntakeFallback = value
	case "anatomy_semantic":
		m.AnatomySemantic = value
	case "planner":
		m.Planner = value
	case "reviewer":
		m.Reviewer = value
	case "cross_ref":
		m.CrossRef = value
	case "adversary":
		m.Adversary = value
	case "coverage_gate":
		m.CoverageGate = value
	case "dedup_gate":
		m.DedupGate = value
	default:
		return false
	}
	return true
}

// ReviewConfig ports config.py ReviewConfig — the top-level config combining all
// sub-configs. The Go `Model` field carries the json tag "models" to match the
// Python field name.
type ReviewConfig struct {
	Budget   BudgetConfig  `json:"budget"`
	Model    ModelConfig   `json:"models"`
	Scoring  ScoringConfig `json:"scoring"`
	Comments CommentConfig `json:"comments"`
	HITL     HITLConfig    `json:"hitl"`

	IgnorePaths []string         `json:"ignore_paths"`
	Hints       []string         `json:"hints"`
	DepthRules  []map[string]any `json:"depth_rules"`
}

// DefaultIgnorePaths is the config.py glob ignore list. Returned fresh per call.
func DefaultIgnorePaths() []string {
	return []string{
		"*.md",
		"*.txt",
		".github/**",
		"vendor/**",
		"node_modules/**",
		"**/*.generated.*",
		"**/*.min.js",
		"**/*.min.css",
		"**/package-lock.json",
		"**/yarn.lock",
		"**/poetry.lock",
	}
}

// DefaultReviewConfig assembles a ReviewConfig from the sub-config defaults
// (the Go analog of pydantic's ReviewConfig() with its default_factory fields).
func DefaultReviewConfig() ReviewConfig {
	return ReviewConfig{
		Budget:      DefaultBudgetConfig(),
		Model:       DefaultModelConfig(),
		Scoring:     DefaultScoringConfig(),
		Comments:    DefaultCommentConfig(),
		HITL:        DefaultHITLConfig(),
		IgnorePaths: DefaultIgnorePaths(),
		Hints:       []string{},
		DepthRules:  []map[string]any{},
	}
}

// FromInput ports ReviewConfig.from_input — it merges per-call API overrides
// into the defaults. The value receiver is ignored (matching the Python
// classmethod semantics: it always starts from a fresh default config).
//
// Budget caps: Python resolves max_cost_usd / max_duration_seconds in review()
// BEFORE constructing the ReviewInput, so from_input always writes a concrete
// resolved value over the BudgetConfig defaults (which is why the 1800-second
// BudgetConfig default is dead — the per-call value is 300 by default). The Go
// ReviewInput carries these as *float64 / *int, so FromInput runs the same
// ResolveBudgetCaps cascade (explicit > env > 2.0/300) to reproduce that
// effective value exactly.
func (ReviewConfig) FromInput(in schemas.ReviewInput) ReviewConfig {
	c := DefaultReviewConfig()

	cost, dur := ResolveBudgetCaps(in.MaxCostUSD, in.MaxDurationSeconds)
	c.Budget.MaxCostUSD = cost
	c.Budget.MaxDurationSeconds = dur

	if in.MaxConcurrentReviewers != nil {
		c.Budget.MaxConcurrentReviewers = *in.MaxConcurrentReviewers
	}
	if in.MaxCoverageIterations != nil {
		c.Budget.MaxCoverageIterations = *in.MaxCoverageIterations
	}
	// max_review_depth clamped to 3 (1=flat, 2=one sub-level, 3=max). review()
	// also clamps at the node-bind layer, so this is the second of two clamps.
	c.Budget.MaxReviewDepth = min(in.MaxReviewDepth, 3)

	if len(in.Models) > 0 {
		for field, modelID := range in.Models {
			c.Model.setByJSONName(field, modelID)
		}
	}

	if len(in.IgnorePaths) > 0 {
		c.IgnorePaths = unionSorted(c.IgnorePaths, in.IgnorePaths)
	}

	if len(in.Hints) > 0 {
		// Python replaces (config.hints = review_input.hints), not merges.
		c.Hints = append([]string(nil), in.Hints...)
	}

	if in.SuggestionMode != "" {
		c.Comments.SuggestionMode = in.SuggestionMode
	}

	return c
}

// unionSorted returns the deduplicated union of a and b. Python uses
// list(set(...)), whose iteration order is unspecified (and varies per run); the
// contents are what the contract fixes, so Go sorts the result for a
// deterministic order (safe: ignore_paths feed order-independent glob matching).
func unionSorted(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
