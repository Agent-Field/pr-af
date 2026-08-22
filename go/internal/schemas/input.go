package schemas

// This file ports schemas/input.py — the pipeline entry-point structs.

// ReviewInput is the top-level input to the PR-AF pipeline.
//
// Divergence from schemas/input.py, deliberate (design §C.1): the pydantic
// ReviewInput types max_cost_usd / max_duration_seconds as non-nullable float /
// int with concrete defaults (2.0 / 3600). The Go struct instead mirrors the
// actual node entry point — app.py review()'s signature, where both are
// `... | None = None` — so they are *float64 / *int and stay nil until
// config.ResolveBudgetCaps / config.ReviewConfig.FromInput resolve the
// explicit-arg > env > default (2.0 / 3600) cascade. This keeps the "was a cap
// explicitly supplied?" signal that Python recovers from the review() defaults.
type ReviewInput struct {
	// Mode 1: GitHub PR URL.
	PrURL *string `json:"pr_url"`
	// Mode 2: Raw diff.
	DiffText *string `json:"diff_text"`
	// Mode 3: Local repo.
	RepoPath *string `json:"repo_path"`
	BaseRef  *string `json:"base_ref"`
	HeadRef  *string `json:"head_ref"`

	// Configuration overrides.
	Depth              string   `json:"depth"` // auto | quick | standard | deep
	MaxCostUSD         *float64 `json:"max_cost_usd"`
	MaxDurationSeconds *int     `json:"max_duration_seconds"`
	Focus              string   `json:"focus"` // auto | security | correctness | performance | tests
	IgnorePaths        []string `json:"ignore_paths"`
	Hints              []string `json:"hints"` // Project-specific review hints

	// Model overrides (per-call). Keys match ModelConfig field names.
	Models map[string]string `json:"models"`

	// Budget overrides.
	MaxConcurrentReviewers *int `json:"max_concurrent_reviewers"`
	MaxCoverageIterations  *int `json:"max_coverage_iterations"`
	MaxReviewDepth         int  `json:"max_review_depth"` // 1=flat, 2=one sub-level, 3=max

	// Output.
	OutputFormat string `json:"output_format"` // github | json | sarif | markdown
	DryRun       bool   `json:"dry_run"`       // Don't post to GitHub, just return findings
	PostPRNumber *int   `json:"post_pr_number"`

	// Optional caller identity for local-repository reviews. These values are
	// returned as result metadata and never grant publication capability.
	PublisherJobID string               `json:"publisher_job_id"`
	PullRequest    *PullRequestIdentity `json:"pull_request"`

	// Comment formatting.
	SuggestionMode string `json:"suggestion_mode"` // comment | code
}

// PullRequestIdentity carries display-only caller metadata for a review whose
// source is a local repository checkout rather than a pr_url clone.
type PullRequestIdentity struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

// GitHubPRData is the data fetched from the GitHub API for a pull request.
type GitHubPRData struct {
	Owner          string        `json:"owner"`
	Repo           string        `json:"repo"`
	Number         int           `json:"number"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Labels         []string      `json:"labels"`
	Author         string        `json:"author"`
	BaseSHA        string        `json:"base_sha"`
	HeadSHA        string        `json:"head_sha"`
	CommitMessages []string      `json:"commit_messages"`
	Diff           string        `json:"diff"` // Unified diff text
	ChangedFiles   []ChangedFile `json:"changed_files"`
}

// ChangedFile is a single file changed in the PR.
type ChangedFile struct {
	Path         string  `json:"path"`
	Status       string  `json:"status"` // added | modified | removed | renamed
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Patch        string  `json:"patch"`         // Unified diff patch for this file
	PreviousPath *string `json:"previous_path"` // For renames
}
