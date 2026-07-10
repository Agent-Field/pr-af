package config

// ScoringConfig ports config.py ScoringConfig — deterministic scoring weights
// and multipliers (§D numeric tables). LLMs reason about issues; code computes
// scores, so the same findings always produce the same scores. Maps are
// returned fresh per call so a caller cannot mutate a shared default.
type ScoringConfig struct {
	BaseWeights          map[string]float64 `json:"base_weights"`
	Multipliers          map[string]float64 `json:"multipliers"`
	ConfidenceThresholds map[string]float64 `json:"confidence_thresholds"`
}

// DefaultScoringConfig builds the config.py scoring defaults.
func DefaultScoringConfig() ScoringConfig {
	return ScoringConfig{
		BaseWeights: map[string]float64{
			"critical":   1.0,
			"important":  0.7,
			"suggestion": 0.3,
			"nitpick":    0.1,
		},
		Multipliers: map[string]float64{
			"cross_ref_compound":   1.5, // Cross-ref found compound risk
			"adversary_confirmed":  1.3, // Adversary confirmed exploitation
			"adversary_challenged": 0.5, // Adversary successfully challenged
			"ai_generated_pr":      1.2, // Extra weight for AI-generated PRs
			"blast_radius_high":    1.2, // Change affects many files (>10)
		},
		ConfidenceThresholds: map[string]float64{
			"critical":   0.2,
			"important":  0.3,
			"suggestion": 0.4,
			"nitpick":    0.4,
		},
	}
}
