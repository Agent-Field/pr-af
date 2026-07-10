package schemas

// This file ports schemas/pipeline.py — the structs that flow between pipeline
// phases. Field order follows the Python declarations so model_dump()-style key
// ordering is reproduced for downstream golden comparisons.

// IntakeResult is Phase 1 output: structured fields for routing plus a
// pr_summary string for LLM context. All fields are required in Python (no
// defaults) so none are seeded.
type IntakeResult struct {
	PrType       string   `json:"pr_type"`    // feature | bugfix | refactor | docs | infra | mixed
	Complexity   string   `json:"complexity"` // trivial | standard | complex | massive
	Languages    []string `json:"languages"`
	AreasTouched []string `json:"areas_touched"` // auth, database, api, frontend, config...
	RiskSignals  []string `json:"risk_signals"`  // "touches auth", "modifies schema", ...
	AIGenerated  float64  `json:"ai_generated"`  // 0.0-1.0 confidence
	ReviewDepth  string   `json:"review_depth"`  // quick | standard | deep
	PrSummary    string   `json:"pr_summary"`    // Brief narrative (string for LLM context)
}

// Hunk is a single diff hunk within a file.
type Hunk struct {
	OldStart int    `json:"old_start"`
	OldCount int    `json:"old_count"`
	NewStart int    `json:"new_start"`
	NewCount int    `json:"new_count"`
	Header   string `json:"header"`  // @@ line
	Content  string `json:"content"` // The actual diff content
}

// FileChange is a programmatic representation of a single file change.
type FileChange struct {
	Path         string `json:"path"`
	Status       string `json:"status"` // added | modified | removed | renamed
	Language     string `json:"language"`
	LinesAdded   int    `json:"lines_added"`
	LinesRemoved int    `json:"lines_removed"`
	Hunks        []Hunk `json:"hunks"`
}

// ChangeCluster is a group of related file changes (e.g. all auth-related files).
type ChangeCluster struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`  // Human-readable cluster name
	Files           []string `json:"files"` // File paths in this cluster
	PrimaryLanguage string   `json:"primary_language"`
	Description     string   `json:"description"`
}

// DiffStats holds aggregate statistics about the diff.
type DiffStats struct {
	TotalFiles       int     `json:"total_files"`
	TotalAdditions   int     `json:"total_additions"`
	TotalDeletions   int     `json:"total_deletions"`
	FilesAdded       int     `json:"files_added"`
	FilesModified    int     `json:"files_modified"`
	FilesRemoved     int     `json:"files_removed"`
	FilesRenamed     int     `json:"files_renamed"`
	TestFilesChanged int     `json:"test_files_changed"`
	TestToCodeRatio  float64 `json:"test_to_code_ratio"`
}

// AnatomyResult is Phase 2 output: structured clusters for routing plus strings
// for LLM context.
type AnatomyResult struct {
	// Structured — consumed by planner for routing.
	Files           []FileChange        `json:"files"`
	Clusters        []ChangeCluster     `json:"clusters"`
	BlastRadius     []string            `json:"blast_radius"`     // Files affected but not changed
	DependencyGraph map[string][]string `json:"dependency_graph"` // file -> [files that import it]
	Stats           DiffStats           `json:"stats"`

	// String — consumed by planner LLM for reasoning.
	PrNarrative      string   `json:"pr_narrative"`
	RiskSurfaces     []string `json:"risk_surfaces"`
	UnrelatedChanges []string `json:"unrelated_changes"`
	IntentGaps       []string `json:"intent_gaps"`
	ContextNotes     string   `json:"context_notes"`
}

// BudgetAllocation is a budget cap for an agent or phase. Seeded (defaults.go):
// max_cost_usd=0.5, max_duration_seconds=60, max_reference_follows=3,
// max_child_spawns=2.
type BudgetAllocation struct {
	MaxCostUSD          float64 `json:"max_cost_usd"`
	MaxDurationSeconds  int     `json:"max_duration_seconds"`
	MaxReferenceFollows int     `json:"max_reference_follows"`
	MaxChildSpawns      int     `json:"max_child_spawns"`
}

// ReviewDimension is one parallel reviewer instance. Seeded (defaults.go):
// priority=1, budget=default BudgetAllocation, context_files=[].
type ReviewDimension struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`          // Human-readable (attributed in comments)
	ReviewPrompt string           `json:"review_prompt"` // Dynamically crafted (reviewer LLM prompt)
	TargetFiles  []string         `json:"target_files"`
	ContextFiles []string         `json:"context_files"`
	Priority     int              `json:"priority"` // Higher = more important = gets budget first
	Budget       BudgetAllocation `json:"budget"`
}

// SubReviewRequest is a reviewer's request to spawn a deeper sub-review. Seeded
// (defaults.go): priority=1, context_files=[].
type SubReviewRequest struct {
	Reason       string   `json:"reason"`
	ReviewPrompt string   `json:"review_prompt"`
	TargetFiles  []string `json:"target_files"`
	ContextFiles []string `json:"context_files"`
	Priority     int      `json:"priority"`
}

// ReviewPlan is Phase 3 output: the planner's complete review strategy.
type ReviewPlan struct {
	Dimensions    []ReviewDimension `json:"dimensions"`
	CrossRefHints []string          `json:"cross_ref_hints"` // Suspected interactions (string for LLM)
	AIAdjusted    bool              `json:"ai_adjusted"`     // Whether plan was adjusted for AI-generated code
	TotalBudget   BudgetAllocation  `json:"total_budget"`
}

// ReviewFinding is emitted to the findings queue as reviewers work. Seeded
// (defaults.go): confidence=0.5, severity="suggestion", tags=[]. Suggestion is
// Optional (nil -> null).
type ReviewFinding struct {
	DimensionID   string   `json:"dimension_id"`
	DimensionName string   `json:"dimension_name"`
	FilePath      string   `json:"file_path"`
	LineStart     int      `json:"line_start"`
	LineEnd       int      `json:"line_end"`
	HunkContext   string   `json:"hunk_context"` // Code context around the finding
	Severity      Severity `json:"severity"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`       // Detailed explanation (GitHub comment)
	Suggestion    *string  `json:"suggestion"` // Concrete fix (code block)
	Evidence      string   `json:"evidence"`   // Code references supporting this finding
	Confidence    float64  `json:"confidence"`
	Tags          []string `json:"tags"` // security, correctness, ...
}

// AdversaryResult is the adversary reviewer's assessment of a finding. Seeded
// (defaults.go): severity_adjustment="none". HiddenTrap is Optional (nil ->
// null).
type AdversaryResult struct {
	FindingTitle       string  `json:"finding_title"`
	Verdict            string  `json:"verdict"` // confirmed | challenged | missed_trap
	Reason             string  `json:"reason"`
	SeverityAdjustment string  `json:"severity_adjustment"` // boost | discount | none
	HiddenTrap         *string `json:"hidden_trap"`         // set when verdict is missed_trap
}

// MetaDimensionResult is the output of a meta-dimension selector (Semantic,
// Mechanical, or Systemic). Seeded (defaults.go): confidence=0.7. Dimensions is
// a required field in Python (no default) so it is not seeded.
type MetaDimensionResult struct {
	Lens       string            `json:"lens"` // "semantic" | "mechanical" | "systemic"
	Dimensions []ReviewDimension `json:"dimensions"`
	Confidence float64           `json:"confidence"` // How complete this lens's coverage is (0-1)
	Rationale  string            `json:"rationale"`
}
