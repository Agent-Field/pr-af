"""Flat schemas for .ai() gate calls.

All gate schemas follow the CLAUDE.md constraint: flat, 2-4 attributes.
These are the fast, cheap, single-shot classifications.
"""

from __future__ import annotations

from pydantic import BaseModel, Field


class IntakeGate(BaseModel):
    """Fast PR classification. Escalates to .harness() if not confident."""

    pr_type: str = Field(
        description='One of: "feature", "bugfix", "refactor", "docs", "infra", "mixed"'
    )
    complexity: str = Field(
        description='One of: "trivial", "standard", "complex", "massive"'
    )
    confident: bool = Field(
        description="Whether classification is confident enough to proceed"
    )


class CoverageGate(BaseModel):
    """Checks whether the review plan covered all change clusters."""

    fully_covered: bool = Field(description="Whether all change clusters were reviewed")
    gap_descriptions: list[str] = Field(
        default_factory=list,
        description="Descriptions of uncovered areas that need gap reviewers",
    )
    confident: bool = True


class DedupGate(BaseModel):
    """Near-duplicate detection between two findings with similar descriptions."""

    is_duplicate: bool = Field(
        description="Whether finding B is a duplicate of finding A"
    )
    keep: str = Field(description='Which to keep: "a", "b", or "both"')
    reason: str = Field(description="Brief explanation of the decision")


class FindingRelevanceGate(BaseModel):
    """Classifies whether a finding is a real functional bug or noise."""

    category: str = Field(
        description='One of: "real_bug", "style_preference", "design_opinion", "false_positive"'
    )
    confident: bool = Field(
        description="Whether the classification is confident"
    )


class OutputCalibrationGate(BaseModel):
    """Final calibration: which findings are worth posting as comments."""

    keep_indices: list[int] = Field(
        description="Indices of findings to keep (0-based)"
    )
    reasoning: str = Field(
        description="Brief explanation of what was dropped and why"
    )
