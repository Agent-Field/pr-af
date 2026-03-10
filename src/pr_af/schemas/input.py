"""Input schemas for PR-AF.

These define the entry point data structures — what the system accepts.
"""

from __future__ import annotations

from pydantic import BaseModel, Field


class ReviewInput(BaseModel):
    """Top-level input to the PR-AF pipeline."""

    # Mode 1: GitHub PR URL
    pr_url: str | None = None

    # Mode 2: Raw diff
    diff_text: str | None = None

    # Mode 3: Local repo
    repo_path: str | None = None
    base_ref: str | None = None
    head_ref: str | None = None

    # Configuration overrides
    depth: str = "auto"  # auto | quick | standard | deep
    max_cost_usd: float = 2.0
    max_duration_seconds: int = 300
    focus: str = "auto"  # auto | security | correctness | performance | tests
    ignore_paths: list[str] = Field(default_factory=list)
    hints: list[str] = Field(default_factory=list)  # Project-specific review hints

    # Model overrides (per-call API variable)
    # Keys match ModelConfig field names: intake_gate, planner, reviewer, etc.
    # Values are model identifiers (e.g. "anthropic/claude-sonnet-4", "openai/gpt-4o")
    # Unset keys fall back to defaults from env or ReviewConfig.
    models: dict[str, str] | None = None

    # Budget overrides
    max_concurrent_reviewers: int | None = None
    max_coverage_iterations: int | None = None
    max_review_depth: int = 2  # Max recursive sub-review depth (1=flat, 2=one sub-level, 3=max)

    # Output
    output_format: str = "github"  # github | json | sarif | markdown
    dry_run: bool = False  # Don't post to GitHub, just return findings
    post_pr_number: int | None = None  # For local repo mode: which PR to post to


class GitHubPRData(BaseModel):
    """Data fetched from GitHub API for a pull request."""

    owner: str
    repo: str
    number: int
    title: str
    description: str
    labels: list[str] = Field(default_factory=list)
    author: str = ""
    base_sha: str = ""
    head_sha: str = ""
    commit_messages: list[str] = Field(default_factory=list)
    diff: str = ""  # Unified diff text
    changed_files: list[ChangedFile] = Field(default_factory=list)


class ChangedFile(BaseModel):
    """A single file changed in the PR."""

    path: str
    status: str  # added | modified | removed | renamed
    additions: int = 0
    deletions: int = 0
    patch: str = ""  # Unified diff patch for this file
    previous_path: str | None = None  # For renames
