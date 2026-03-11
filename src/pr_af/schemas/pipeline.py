"""Inter-agent pipeline schemas.

These define the data that flows between pipeline phases.
Format choices follow the archei rules from CLAUDE.md:
  - Structured JSON: consumed by code for routing/decisions
  - String: consumed by downstream LLM agents
  - Hybrid: both
"""

from __future__ import annotations

from pydantic import BaseModel, Field

# ---------------------------------------------------------------------------
# Phase 1 → Phase 3: Intake Result
# Format: Hybrid (structured fields for routing + pr_summary string for LLM context)
# ---------------------------------------------------------------------------


class IntakeResult(BaseModel):
    """Phase 1 output. Hybrid — structured fields for routing, pr_summary string for LLM context."""

    pr_type: str  # feature | bugfix | refactor | docs | infra | mixed
    complexity: str  # trivial | standard | complex | massive
    languages: list[str]
    areas_touched: list[str]  # semantic areas: auth, database, api, frontend, config...
    risk_signals: list[str]  # "touches auth", "modifies schema", "changes API contract"
    ai_generated: float  # 0.0-1.0 confidence
    review_depth: str  # quick | standard | deep
    pr_summary: str  # Brief narrative of what the PR does (string for LLM context)


# ---------------------------------------------------------------------------
# Phase 2 → Phase 3: Anatomy Result
# Format: Hybrid (structured for routing + string for LLM context)
# ---------------------------------------------------------------------------


class FileChange(BaseModel):
    """Programmatic representation of a single file change."""

    path: str
    status: str  # added | modified | removed | renamed
    language: str = ""
    lines_added: int = 0
    lines_removed: int = 0
    hunks: list[Hunk] = Field(default_factory=list)


class Hunk(BaseModel):
    """A single diff hunk within a file."""

    old_start: int
    old_count: int
    new_start: int
    new_count: int
    header: str = ""  # @@ line
    content: str = ""  # The actual diff content


class ChangeCluster(BaseModel):
    """Group of related file changes (e.g., all auth-related files)."""

    id: str
    name: str  # Human-readable cluster name
    files: list[str]  # File paths in this cluster
    primary_language: str = ""
    description: str = ""  # Brief description of what this cluster changes


class DiffStats(BaseModel):
    """Aggregate statistics about the diff."""

    total_files: int = 0
    total_additions: int = 0
    total_deletions: int = 0
    files_added: int = 0
    files_modified: int = 0
    files_removed: int = 0
    files_renamed: int = 0
    test_files_changed: int = 0
    test_to_code_ratio: float = 0.0


class AnatomyResult(BaseModel):
    """Phase 2 output. Hybrid — structured clusters for routing, strings for LLM context."""

    # Structured: consumed by planner for routing
    files: list[FileChange]
    clusters: list[ChangeCluster]
    blast_radius: list[str]  # Files affected but not changed
    dependency_graph: dict[str, list[str]]  # file → [files that import it]
    stats: DiffStats

    # String: consumed by planner LLM for reasoning
    pr_narrative: str  # "What this PR does and why"
    risk_surfaces: list[str]  # Semantic risk areas identified
    unrelated_changes: list[str]  # Files that don't fit the PR story
    intent_gaps: list[str]  # Claimed in description but not in diff (or vice versa)
    context_notes: str = ""  # Additional context for downstream reviewers


# ---------------------------------------------------------------------------
# Phase 3 → Phase 4: Review Plan
# Format: Hybrid (structured dimensions for routing, string prompts for LLMs)
# ---------------------------------------------------------------------------


class BudgetAllocation(BaseModel):
    """Budget cap for an agent or phase."""

    max_cost_usd: float = 0.5
    max_duration_seconds: int = 60
    max_reference_follows: int = 3
    max_child_spawns: int = 2


class ReviewDimension(BaseModel):
    """A single review dimension — becomes one parallel reviewer instance.

    The review_prompt is THE key innovation: dynamically crafted by the planner
    at runtime, not selected from a fixed catalog.
    """

    id: str
    name: str  # Human-readable name (attributed in comments)
    review_prompt: str  # Dynamically crafted prompt (string — consumed by reviewer LLM)
    target_files: list[str]  # Files this reviewer must examine
    context_files: list[str] = Field(default_factory=list)  # Additional files for reference
    priority: int = 1  # Higher = more important = gets budget first
    budget: BudgetAllocation = Field(default_factory=BudgetAllocation)


class SubReviewRequest(BaseModel):
    """A request from a reviewer to spawn a deeper sub-review on a specific area.

    Reviewers emit these when they discover a complex area that requires
    specialized deeper analysis beyond their current scope.
    """

    reason: str  # Why this sub-review is needed
    review_prompt: str  # Crafted prompt for the child reviewer
    target_files: list[str]  # Files the child should inspect
    context_files: list[str] = Field(default_factory=list)
    priority: int = 1


class ReviewPlan(BaseModel):
    """Phase 3 output. The planner's complete review strategy."""

    dimensions: list[ReviewDimension]
    cross_ref_hints: list[str] = Field(default_factory=list)  # Suspected interactions (string for LLM)
    ai_adjusted: bool = False  # Whether plan was adjusted for AI-generated code
    total_budget: BudgetAllocation = Field(default_factory=BudgetAllocation)


# ---------------------------------------------------------------------------
# Phase 4 → Phase 5: Review Findings (streaming via asyncio.Queue)
# Format: Structured JSON (consumed by scoring code + string fields for LLM)
# ---------------------------------------------------------------------------


class ReviewFinding(BaseModel):
    """Emitted to findings queue as reviewers work."""

    dimension_id: str
    dimension_name: str
    file_path: str
    line_start: int
    line_end: int
    hunk_context: str = ""  # Code context around the finding
    severity: str  # critical | important | suggestion | nitpick
    title: str
    body: str  # Detailed explanation (string — appears in GitHub comment)
    suggestion: str | None = None  # Concrete fix (code block)
    evidence: str = ""  # Code references that support this finding
    confidence: float = 0.5
    tags: list[str] = Field(default_factory=list)  # Machine-readable: security, correctness, etc.


# ---------------------------------------------------------------------------
# Phase 5 → Phase 6: Adversary + Cross-Ref results
# Format: Structured JSON (consumed by scoring code)
# ---------------------------------------------------------------------------


class CrossRefInteraction(BaseModel):
    """A cross-reference interaction between findings from different reviewers."""

    finding_a_title: str
    finding_b_title: str
    interaction_type: str  # compound_risk | assumption_violation | consistency_gap
    description: str  # How they interact
    combined_severity: str  # The severity of the combined issue
    file_paths: list[str]
    line_references: list[str] = Field(default_factory=list)


class AdversaryResult(BaseModel):
    """Adversary reviewer's assessment of a finding."""

    finding_title: str
    verdict: str  # confirmed | challenged | missed_trap
    reason: str
    severity_adjustment: str = "none"  # boost | discount | none
    hidden_trap: str | None = None  # If verdict is missed_trap, the trap description


# ---------------------------------------------------------------------------
# Phase 6 → Phase 7: Meta-Dimension Selection Results
# Format: Structured JSON (consumed by meta-selector orchestration)
# ---------------------------------------------------------------------------


class MetaDimensionResult(BaseModel):
    """Output of a meta-dimension selector (Semantic, Mechanical, or Systemic).

    Each meta-selector produces a list of ReviewDimension objects plus
    a confidence assessment of completeness for its lens.
    """

    lens: str  # "semantic" | "mechanical" | "systemic"
    dimensions: list[ReviewDimension]  # The generated review dimensions
    confidence: float = 0.7  # How complete this lens's coverage is (0-1)
    rationale: str = ""  # Brief explanation of dimension choices


class MetaSelectorConfig(BaseModel):
    """Configuration for meta-dimension selectors. Passed per-call via API."""

    enabled_lenses: list[str] = Field(default_factory=lambda: ["semantic", "mechanical", "systemic"])
    confidence_threshold: float = 0.6  # Minimum confidence for a finding to pass Level 2 filter
    adversary_batch_size: int = 5  # How many findings per parallel adversary batch
    max_adversary_batches: int = 4  # Hard cap on parallel adversary instances
