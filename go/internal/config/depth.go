package config

// DepthProfile ports config.py DepthProfile — a pre-built review-depth profile.
type DepthProfile struct {
	MaxDimensions int    `json:"max_dimensions"`
	ModelTier     string `json:"model_tier"` // budget | standard | premium
}

// DepthProfiles ports config.py DEPTH_PROFILES. These are static (no env), so a
// package var is fine.
var DepthProfiles = map[string]DepthProfile{
	"quick":    {MaxDimensions: 3, ModelTier: "budget"},
	"standard": {MaxDimensions: 6, ModelTier: "standard"},
	"deep":     {MaxDimensions: 12, ModelTier: "premium"},
}

// AutoDepthThreshold maps a lines-changed ceiling to a depth. Ordered ascending;
// the first threshold whose Lines value the change count is under selects the
// depth. A change over the last threshold (>500) falls through to "deep".
type AutoDepthThreshold struct {
	Lines int
	Depth string
}

// AutoDepthThresholds ports config.py AUTO_DEPTH_THRESHOLDS: <100 -> quick,
// 100-500 -> standard, >500 -> deep. Kept as an ordered slice (Python relied on
// insertion order of the {100:..,500:..} dict) so consumers can range in order.
var AutoDepthThresholds = []AutoDepthThreshold{
	{Lines: 100, Depth: "quick"},    // < 100 lines -> quick
	{Lines: 500, Depth: "standard"}, // 100-500 lines -> standard
	// > 500 lines -> deep (fall-through)
}
