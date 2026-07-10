package config

import "strings"

// CommentConfig ports config.py CommentConfig — comment formatting and posting
// preferences (§D numeric tables). PostWorthinessGate is env-driven (default
// OFF); every other default is a plain constant.
type CommentConfig struct {
	MinSeverity                 string `json:"min_severity"` // Minimum severity to include
	MaxComments                 int    `json:"max_comments"` // Cap inline comments
	IncludeSuggestions          bool   `json:"include_suggestions"`
	IncludeDimensionAttribution bool   `json:"include_dimension_attribution"`
	IncludeConfidence           bool   `json:"include_confidence"`
	SuggestionMode              string `json:"suggestion_mode"` // comment | code

	// PolishEnabled runs a parallel .ai() pass that rewrites each comment body
	// right before posting; on any per-call failure the original body is kept.
	PolishEnabled bool `json:"polish_enabled"`

	// MergeGateEnabled runs a parallel .ai() pass classifying each finding as
	// blocking vs advisory. Default ON; failures default to advisory.
	MergeGateEnabled bool `json:"merge_gate_enabled"`

	// PostWorthinessGate: an experienced-reviewer precision pass. DEFAULT OFF;
	// enabled by PR_AF_POSTWORTHINESS_GATE in {"1","true","yes"}.
	PostWorthinessGate bool `json:"post_worthiness_gate"`

	SeverityEmojis map[string]string `json:"severity_emojis"`
}

// DefaultCommentConfig builds the config.py comment defaults, reading
// PR_AF_POSTWORTHINESS_GATE at call time.
func DefaultCommentConfig() CommentConfig {
	return CommentConfig{
		MinSeverity:                 "nitpick",
		MaxComments:                 25,
		IncludeSuggestions:          true,
		IncludeDimensionAttribution: true,
		IncludeConfidence:           true,
		SuggestionMode:              "comment",
		PolishEnabled:               true,
		MergeGateEnabled:            true,
		PostWorthinessGate:          postWorthinessDefault(),
		SeverityEmojis: map[string]string{
			"critical":   "🔴",
			"important":  "🟠",
			"suggestion": "🔵",
			"nitpick":    "⚪",
		},
	}
}

// postWorthinessDefault ports the PR_AF_POSTWORTHINESS_GATE default_factory
// (default OFF, enabled only by "1"/"true"/"yes").
func postWorthinessDefault() bool {
	switch strings.ToLower(strEnv("PR_AF_POSTWORTHINESS_GATE", "")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
