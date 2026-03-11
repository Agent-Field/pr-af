"""Configuration for PR-AF.

All behavioral tuning in one place. Model assignments, budget caps, loop limits,
comment formatting, review strictness. Most changes start here.

Follows the Contract-AF config pattern: centralized, typed, auditable.
"""

from __future__ import annotations

import os
import tempfile
from typing import TYPE_CHECKING

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from .schemas.input import ReviewInput


class BudgetConfig(BaseModel):
    """Global and per-phase budget caps."""

    # Global caps
    max_cost_usd: float = 2.0
    max_duration_seconds: int = 1800

    # Phase-level cost allocation (USD)
    phase_budgets: dict[str, float] = Field(
        default_factory=lambda: {
            "intake": 0.05,
            "anatomy": 0.15,
            "meta_selectors": 0.30,  # 3 parallel lenses
            "review": 0.90,  # Most budget goes here
            "adversary": 0.40,  # Parallel batches
            "cross_ref": 0.30,
            "coverage": 0.10,
            "synthesis": 0.00,  # Code, no LLM cost
            "output": 0.00,  # Code, no LLM cost
        }
    )

    # Concurrency
    max_concurrent_reviewers: int = 8

    # Inner loop caps (per-reviewer)
    max_reference_follows_per_reviewer: int = 3
    max_child_spawns_per_reviewer: int = 2

    # Middle loop caps (cross-agent)
    max_cross_ref_deep_dives: int = 5

    # Outer loop caps (pipeline)
    max_coverage_iterations: int = 2

    # Recursive sub-review depth (1=flat, 2=one sub-level, 3=max)
    max_review_depth: int = 2


class ModelConfig(BaseModel):
    """Model routing per agent.

    Philosophy: budget models for gates/classification,
    premium models for planning/reviewing/challenging.
    Plan quality = review quality, so planner gets premium.
    """

    intake_gate: str = "budget"  # .ai() fast classification
    intake_fallback: str = "mid"  # .harness() when not confident
    anatomy_semantic: str = "mid"  # Narrative understanding
    planner: str = "premium"  # THE critical agent: plan quality = review quality
    reviewer: str = "premium"  # Deep code analysis
    cross_ref: str = "premium"  # Interaction detection needs best reasoning
    adversary: str = "premium"  # Challenge quality matters
    coverage_gate: str = "budget"  # Simple completeness check
    dedup_gate: str = "budget"  # Near-duplicate detection


class ScoringConfig(BaseModel):
    """Deterministic scoring weights and multipliers.

    LLMs reason about issues; code computes scores.
    Same findings always produce same scores.
    """

    base_weights: dict[str, float] = Field(
        default_factory=lambda: {
            "critical": 1.0,
            "important": 0.7,
            "suggestion": 0.3,
            "nitpick": 0.1,
        }
    )

    multipliers: dict[str, float] = Field(
        default_factory=lambda: {
            "cross_ref_compound": 1.5,  # Cross-ref found compound risk
            "adversary_confirmed": 1.3,  # Adversary confirmed exploitation
            "adversary_challenged": 0.5,  # Adversary successfully challenged
            "ai_generated_pr": 1.2,  # Extra weight for AI-generated PRs
            "blast_radius_high": 1.2,  # Change affects many files (>10)
        }
    )

    confidence_thresholds: dict[str, float] = Field(
        default_factory=lambda: {
            "critical": 0.2,
            "important": 0.3,
            "suggestion": 0.4,
            "nitpick": 0.4,
        }
    )


class CommentConfig(BaseModel):
    """Comment formatting and posting preferences."""

    min_severity: str = "nitpick"  # Minimum severity to include in summary/comments
    max_comments: int = 25  # Cap inline comments to avoid overwhelming
    include_suggestions: bool = True  # Include ```suggestion blocks
    include_dimension_attribution: bool = True  # Show which dimension found it
    include_confidence: bool = True  # Show confidence score
    suggestion_mode: str = "comment"  # comment | code

    severity_emojis: dict[str, str] = Field(
        default_factory=lambda: {
            "critical": "🔴",
            "important": "🟠",
            "suggestion": "🔵",
            "nitpick": "⚪",
        }
    )

    # Review event logic
    # Any critical → REQUEST_CHANGES
    # Important only → COMMENT
    # Suggestions/nitpicks only → APPROVE with comments
    # Nothing → APPROVE clean


class DepthProfile(BaseModel):
    """Pre-built profiles for review depth."""

    max_dimensions: int = 6
    model_tier: str = "standard"  # budget | standard | premium


DEPTH_PROFILES: dict[str, DepthProfile] = {
    "quick": DepthProfile(max_dimensions=3, model_tier="budget"),
    "standard": DepthProfile(max_dimensions=6, model_tier="standard"),
    "deep": DepthProfile(max_dimensions=12, model_tier="premium"),
}

# Auto-depth thresholds (lines changed → depth)
AUTO_DEPTH_THRESHOLDS = {
    100: "quick",  # < 100 lines → quick
    500: "standard",  # 100-500 lines → standard
    # > 500 lines → deep
}


class ReviewConfig(BaseModel):
    """Top-level configuration combining all sub-configs."""

    budget: BudgetConfig = Field(default_factory=BudgetConfig)
    models: ModelConfig = Field(default_factory=ModelConfig)
    scoring: ScoringConfig = Field(default_factory=ScoringConfig)
    comments: CommentConfig = Field(default_factory=CommentConfig)

    # File ignore patterns (glob)
    ignore_paths: list[str] = Field(
        default_factory=lambda: [
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
        ]
    )

    # Project-specific review hints (passed to planner as additional context)
    # These are NOT hardcoded rules — the planner decides how to use them.
    hints: list[str] = Field(default_factory=list)

    # Depth override rules
    depth_rules: list[dict] = Field(default_factory=list)

    @classmethod
    def from_input(cls, review_input: ReviewInput) -> ReviewConfig:
        """Merge per-call API overrides into defaults (SEC-AF pattern)."""
        config = cls()

        config.budget.max_cost_usd = review_input.max_cost_usd
        config.budget.max_duration_seconds = review_input.max_duration_seconds
        if review_input.max_concurrent_reviewers is not None:
            config.budget.max_concurrent_reviewers = review_input.max_concurrent_reviewers
        if review_input.max_coverage_iterations is not None:
            config.budget.max_coverage_iterations = review_input.max_coverage_iterations
        config.budget.max_review_depth = min(review_input.max_review_depth, 3)

        if review_input.models:
            for field_name, model_id in review_input.models.items():
                if hasattr(config.models, field_name):
                    setattr(config.models, field_name, model_id)

        if review_input.ignore_paths:
            config.ignore_paths = list(set(config.ignore_paths + review_input.ignore_paths))

        if review_input.hints:
            config.hints = review_input.hints

        if hasattr(review_input, "suggestion_mode") and review_input.suggestion_mode:
            config.comments.suggestion_mode = review_input.suggestion_mode

        return config

    @classmethod
    def from_yaml(cls, path: str) -> ReviewConfig:
        """Load config from .pr-af.yml file."""
        from pathlib import Path as _Path

        import yaml

        config_path = _Path(path)
        if not config_path.exists():
            return cls()

        with config_path.open() as f:
            data = yaml.safe_load(f) or {}

        return cls(**data)


class AIIntegrationConfig(BaseModel):
    provider: str = Field(
        default_factory=lambda: os.getenv("PR_AF_PROVIDER", os.getenv("HARNESS_PROVIDER", "opencode"))
    )
    harness_model: str = Field(
        default_factory=lambda: os.getenv("PR_AF_MODEL", os.getenv("HARNESS_MODEL", "minimax/minimax-m2.5"))
    )
    ai_model: str = Field(
        default_factory=lambda: os.getenv(
            "PR_AF_AI_MODEL",
            os.getenv("AI_MODEL", os.getenv("PR_AF_MODEL", "minimax/minimax-m2.5")),
        )
    )
    max_turns: int = Field(default_factory=lambda: int(os.getenv("PR_AF_MAX_TURNS", "50")))
    max_retries: int = Field(default_factory=lambda: int(os.getenv("PR_AF_AI_MAX_RETRIES", "3")))
    initial_backoff_seconds: float = Field(
        default_factory=lambda: float(os.getenv("PR_AF_AI_INITIAL_BACKOFF_SECONDS", "2.0"))
    )
    max_backoff_seconds: float = Field(default_factory=lambda: float(os.getenv("PR_AF_AI_MAX_BACKOFF_SECONDS", "8.0")))
    opencode_bin: str = Field(default_factory=lambda: os.getenv("PR_AF_OPENCODE_BIN", "opencode"))
    opencode_server: str | None = Field(
        default_factory=lambda: os.getenv("PR_AF_OPENCODE_SERVER", os.getenv("OPENCODE_SERVER"))
    )

    @classmethod
    def from_env(cls) -> AIIntegrationConfig:
        return cls()

    def provider_env(self) -> dict[str, str]:
        env_keys = (
            "OPENROUTER_API_KEY",
            "ANTHROPIC_API_KEY",
            "OPENAI_API_KEY",
            "GOOGLE_API_KEY",
            "GITHUB_TOKEN",
            "GH_TOKEN",
        )
        env: dict[str, str] = {key: value for key in env_keys if (value := os.getenv(key))}
        xdg = os.getenv("XDG_DATA_HOME") or os.path.join(tempfile.gettempdir(), "opencode-shared-data")
        os.makedirs(xdg, exist_ok=True)
        env["XDG_DATA_HOME"] = xdg
        return env
