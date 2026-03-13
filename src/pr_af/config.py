"""Configuration for PR-AF.

All behavioral tuning in one place. Model assignments, budget caps, loop limits,
comment formatting, review strictness. Most changes start here.

Follows the Contract-AF config pattern: centralized, typed, auditable.
"""

from __future__ import annotations

import os
from typing import TYPE_CHECKING, ClassVar

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from .schemas.input import ReviewInput


class BudgetConfig(BaseModel):
    """Global and per-phase budget caps."""

    # Global caps
    max_cost_usd: float = 2.0
    max_duration_seconds: int = 1800

    # Set to True to disable all budget enforcement (global + phase).
    # Useful for cost measurement / benchmarking runs.
    no_budget: bool = False

    # Phase-level cost allocation (USD) — proportions of the global budget.
    # These are the defaults for a $2 global cap.  When max_cost_usd is
    # overridden, phase budgets scale proportionally (see from_input()).
    phase_budgets: dict[str, float] = Field(
        default_factory=lambda: {
            "intake": 0.10,
            "anatomy": 0.20,
            "meta_selectors": 0.30,  # 3 parallel lenses
            "review": 0.80,  # Most budget goes here
            "adversary": 0.30,  # Parallel batches
            "cross_ref": 0.20,
            "coverage": 0.10,
            "synthesis": 0.00,  # Code, no LLM cost
            "output": 0.00,  # Code, no LLM cost
        }
    )

    # Concurrency — kept low to avoid cascading rate-limit backoff
    # when using OpenRouter or other rate-limited providers.
    max_concurrent_reviewers: int = 3

    # Stagger delay (seconds) between launching parallel tasks to avoid
    # burst rate-limit hits.  Set to 0 to disable staggering.
    stagger_delay_seconds: float = 2.0

    # Inner loop caps (per-reviewer)
    max_reference_follows_per_reviewer: int = 3
    max_child_spawns_per_reviewer: int = 2

    # Middle loop caps (cross-agent)
    max_cross_ref_deep_dives: int = 5

    # Outer loop caps (pipeline)
    max_coverage_iterations: int = 2

    # Recursive sub-review depth (1=flat, 2=one sub-level, 3=max)
    max_review_depth: int = 2


def _default_tier_map(provider: str = "opencode") -> dict[str, str]:
    """Build tier→model map from env vars, with provider-appropriate defaults.

    OpenCode uses OpenRouter model IDs; Claude Code uses normalized identifiers
    (haiku, sonnet, opus) that the Claude Agent SDK understands natively.
    """
    if provider == "claude-code":
        return {
            "budget": os.getenv("PR_AF_MODEL_BUDGET", "haiku"),
            "mid": os.getenv("PR_AF_MODEL_MID", "sonnet"),
            "premium": os.getenv("PR_AF_MODEL_PREMIUM", "opus"),
        }
    # opencode / default — OpenRouter model IDs
    ai_model = os.getenv(
        "PR_AF_AI_MODEL",
        os.getenv("AI_MODEL", os.getenv("PR_AF_MODEL", "openrouter/google/gemini-2.5-flash")),
    )
    return {
        "budget": os.getenv("PR_AF_MODEL_BUDGET", "openrouter/google/gemini-2.5-flash"),
        "mid": os.getenv("PR_AF_MODEL_MID", ai_model),
        "premium": os.getenv("PR_AF_MODEL_PREMIUM", ai_model),
    }


def resolve_model_tier(model_spec: str, tier_map: dict[str, str] | None = None) -> str:
    """Resolve a model spec that may be a tier name ('budget', 'mid', 'premium')
    into an actual model ID. Passes through already-qualified model IDs unchanged."""
    if "/" in model_spec:
        return model_spec  # Already a qualified model ID
    tiers = tier_map or _default_tier_map()
    return tiers.get(model_spec, model_spec)


class ModelConfig(BaseModel):
    """Model routing per agent.

    Philosophy: budget models for gates/classification,
    premium models for planning/reviewing/challenging.
    Plan quality = review quality, so planner gets premium.

    Values can be tier names ('budget', 'mid', 'premium') which are resolved
    to actual model IDs via ``resolve_model_tier()`` at access time, or
    fully-qualified model IDs (e.g. 'openrouter/google/gemini-2.5-flash').
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

    # Fields that use .ai() (OpenRouter) instead of .harness() (provider).
    # These always resolve with the OpenRouter tier map regardless of provider.
    _AI_FIELDS: ClassVar[set[str]] = {"intake_gate", "coverage_gate"}

    def resolve(self, provider: str = "opencode") -> ModelConfig:
        """Return a copy with all tier names resolved to actual model IDs."""
        harness_map = _default_tier_map(provider)
        ai_map = _default_tier_map("opencode")  # .ai() always uses OpenRouter
        data = {}
        for field_name in self.model_fields:
            val = getattr(self, field_name)
            tier_map = ai_map if field_name in self._AI_FIELDS else harness_map
            data[field_name] = resolve_model_tier(val, tier_map)
        return ModelConfig(**data)


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
    def from_input(cls, review_input: ReviewInput, provider: str = "opencode") -> ReviewConfig:
        """Merge per-call API overrides into defaults (SEC-AF pattern)."""
        config = cls()

        config.budget.max_cost_usd = review_input.max_cost_usd
        config.budget.max_duration_seconds = review_input.max_duration_seconds
        if review_input.max_concurrent_reviewers is not None:
            config.budget.max_concurrent_reviewers = review_input.max_concurrent_reviewers
        if review_input.max_coverage_iterations is not None:
            config.budget.max_coverage_iterations = review_input.max_coverage_iterations
        config.budget.max_review_depth = min(review_input.max_review_depth, 3)

        # no_budget mode: disable all cost enforcement
        if getattr(review_input, "no_budget", False):
            config.budget.no_budget = True

        # Scale phase budgets proportionally when global cap differs from default.
        # Default phase budgets are calibrated for $2.  If the caller sets $50,
        # each phase gets 25× its default allocation.
        default_global = cls().budget.max_cost_usd  # $2.0
        if review_input.max_cost_usd != default_global:
            scale = review_input.max_cost_usd / default_global
            config.budget.phase_budgets = {
                phase: cap * scale
                for phase, cap in config.budget.phase_budgets.items()
            }

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

        # Resolve tier names ('budget', 'mid', 'premium') → actual model IDs
        config.models = config.models.resolve(provider=provider)

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
        home = os.getenv("HOME", os.path.expanduser("~"))
        xdg = os.getenv("XDG_DATA_HOME") or os.path.join(home, ".local", "share")
        os.makedirs(xdg, exist_ok=True)
        env["XDG_DATA_HOME"] = xdg
        return env
