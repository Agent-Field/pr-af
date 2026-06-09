"""Output schemas for PR-AF.

These define what the pipeline produces — the final deliverables.
"""

from __future__ import annotations

from pydantic import BaseModel, Field

from .severity import Severity  # noqa: TC001 - runtime-needed pydantic field type


class ScoredFinding(BaseModel):
    """A finding after scoring, dedup, and line mapping."""

    id: str
    dimension_id: str
    dimension_name: str
    file_path: str
    line_start: int
    line_end: int
    diff_line: int | None = None  # Line number in the diff (for GitHub API)
    diff_side: str = "RIGHT"  # LEFT (deletion) or RIGHT (addition)
    severity: Severity  # critical | important | suggestion | nitpick (normalized)
    title: str
    body: str
    suggestion: str | None = None
    evidence: str = ""
    confidence: float = 0.5
    tags: list[str] = Field(default_factory=list)
    score: float = 0.0
    active_multipliers: list[str] = Field(default_factory=list)
    # Orthogonal release-blocker axis. Severity = "how bad", blocking = "must fix before merge".
    # Default False so unreviewed findings never falsely block merge.
    blocking: bool = False
    blocking_reason: str = ""


class ReviewSummary(BaseModel):
    """Aggregate statistics about the review."""

    total_findings: int = 0
    by_severity: dict[str, int] = Field(default_factory=dict)
    blocking_count: int = 0
    advisory_count: int = 0
    dimensions_run: int = 0
    cross_ref_interactions: int = Field(
        default=0,
        description="Backward-compatible field name; value now represents synthesized compound findings.",
    )
    adversary_challenged: int = 0
    adversary_confirmed: int = 0
    coverage_iterations: int = 0
    ai_generated_confidence: float = 0.0
    cost_usd: float = 0.0
    duration_seconds: float = 0.0
    budget_exhausted: bool = False


class GitHubComment(BaseModel):
    """A single inline comment for the GitHub PR Review API."""

    path: str
    line: int
    side: str = "RIGHT"
    body: str


class GitHubReview(BaseModel):
    """The complete GitHub PR review payload."""

    body: str  # Executive summary
    event: str  # APPROVE | COMMENT | REQUEST_CHANGES
    comments: list[GitHubComment] = Field(default_factory=list)


class ReviewResult(BaseModel):
    """The complete output of the PR-AF pipeline."""

    review_id: str
    pr_url: str = ""
    review: GitHubReview  # The GitHub review payload
    findings: list[ScoredFinding]  # All scored findings
    summary: ReviewSummary
    metadata: ReviewMetadata


class ReviewMetadata(BaseModel):
    """Pipeline metadata for debugging and observability."""

    intake: dict = Field(default_factory=dict)
    anatomy: dict = Field(default_factory=dict)
    plan: dict = Field(default_factory=dict)
    budget: dict = Field(default_factory=dict)
    agent_invocations: int = 0
    phases_completed: list[str] = Field(default_factory=list)
