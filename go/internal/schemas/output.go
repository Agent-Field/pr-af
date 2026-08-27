package schemas

// This file ports schemas/output.py — the final pipeline deliverables.

// ScoredFinding is a finding after scoring, dedup, and line mapping. Seeded
// (defaults.go): diff_side="RIGHT", confidence=0.5, severity="suggestion",
// tags=[], active_multipliers=[]. DiffLine and Suggestion are Optional (nil ->
// null).
type ScoredFinding struct {
	ID                string   `json:"id"`
	DimensionID       string   `json:"dimension_id"`
	DimensionName     string   `json:"dimension_name"`
	FilePath          string   `json:"file_path"`
	LineStart         int      `json:"line_start"`
	LineEnd           int      `json:"line_end"`
	DiffLine          *int     `json:"diff_line"` // Line number in the diff (GitHub API)
	DiffSide          string   `json:"diff_side"` // LEFT (deletion) | RIGHT (addition)
	Severity          Severity `json:"severity"`
	Title             string   `json:"title"`
	Body              string   `json:"body"`
	Suggestion        *string  `json:"suggestion"`
	Evidence          string   `json:"evidence"`
	Confidence        float64  `json:"confidence"`
	Tags              []string `json:"tags"`
	Score             float64  `json:"score"`
	ActiveMultipliers []string `json:"active_multipliers"`
	// Orthogonal release-blocker axis. Severity = "how bad", blocking = "must
	// fix before merge". Default false so unreviewed findings never falsely
	// block merge.
	Blocking       bool   `json:"blocking"`
	BlockingReason string `json:"blocking_reason"`
}

// ReviewSummary holds aggregate statistics about the review. by_severity is a
// dict default in Python — construct it non-nil upstream to marshal as {}.
type ReviewSummary struct {
	TotalFindings int            `json:"total_findings"`
	BySeverity    map[string]int `json:"by_severity"`
	BlockingCount int            `json:"blocking_count"`
	AdvisoryCount int            `json:"advisory_count"`
	DimensionsRun int            `json:"dimensions_run"`
	// Backward-compatible field name; value now represents synthesized compound
	// findings.
	CrossRefInteractions  int     `json:"cross_ref_interactions"`
	AdversaryChallenged   int     `json:"adversary_challenged"`
	AdversaryConfirmed    int     `json:"adversary_confirmed"`
	CoverageIterations    int     `json:"coverage_iterations"`
	AIGeneratedConfidence float64 `json:"ai_generated_confidence"`
	CostUSD               float64 `json:"cost_usd"`
	DurationSeconds       float64 `json:"duration_seconds"`
	BudgetExhausted       bool    `json:"budget_exhausted"`
}

// GitHubComment is a single inline comment for the GitHub PR Review API. Seeded
// (defaults.go): side="RIGHT".
type GitHubComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// GitHubReview is the complete GitHub PR review payload. comments is a list
// default in Python — construct it non-nil upstream to marshal as [].
type GitHubReview struct {
	Body     string          `json:"body"`  // Executive summary
	Event    string          `json:"event"` // APPROVE | COMMENT | REQUEST_CHANGES
	Comments []GitHubComment `json:"comments"`
}

// ReviewResult is the complete output of the PR-AF pipeline.
type ReviewResult struct {
	ReviewID string          `json:"review_id"`
	PrURL    string          `json:"pr_url"`
	Review   GitHubReview    `json:"review"`   // The GitHub review payload
	Findings []ScoredFinding `json:"findings"` // All scored findings
	Summary  ReviewSummary   `json:"summary"`
	Metadata ReviewMetadata  `json:"metadata"`
}

// ReviewMetadata holds pipeline metadata for debugging and observability. The
// intake/anatomy/plan/budget dicts and phases_completed list are collection
// defaults in Python — construct them non-nil upstream to marshal as {} / [].
type ReviewMetadata struct {
	Intake                     map[string]any `json:"intake"`
	Anatomy                    map[string]any `json:"anatomy"`
	Plan                       map[string]any `json:"plan"`
	Budget                     map[string]any `json:"budget"`
	Request                    map[string]any `json:"request"`
	AgentInvocations           int            `json:"agent_invocations"`
	PipelineContractVersion    int            `json:"pipeline_contract_version"`
	PhasesCompleted            []string       `json:"phases_completed"`
	ReviewDimensionsAttempted  int            `json:"review_dimensions_attempted"`
	ReviewDimensionsParseable  int            `json:"review_dimensions_parseable"`
	DegradedDimensions         int            `json:"degraded_dimensions"`
	EvidenceFindingsEligible   int            `json:"evidence_findings_eligible"`
	EvidenceFindingsAttempted  int            `json:"evidence_findings_attempted"`
	AdversaryFindingsEligible  int            `json:"adversary_findings_eligible"`
	AdversaryFindingsAttempted int            `json:"adversary_findings_attempted"`
}
