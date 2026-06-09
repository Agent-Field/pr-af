"""Deterministic scoring engine for PR-AF.

LLMs reason about issues; this code computes scores.
Same findings always produce same scores. Auditable, testable, tunable.

Follows the Contract-AF / SEC-AF pattern: scoring is intentionally
separated from agents so it can be modified without touching agent code.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from .schemas.output import ScoredFinding

if TYPE_CHECKING:
    from .config import ScoringConfig
    from .schemas.pipeline import AdversaryResult, ReviewFinding


def score_findings(
    findings: list[ReviewFinding],
    adversary_results: list[AdversaryResult],
    config: ScoringConfig,
    ai_generated: float = 0.0,
    blast_radius_size: int = 0,
) -> list[ScoredFinding]:
    """Score, rank, and filter findings.

    Steps:
    1. Apply base severity weights
    2. Apply multipliers from adversary and global context
    3. Filter by confidence thresholds
    4. Sort by composite score descending
    """

    # Index adversary results by finding title
    adversary_by_title: dict[str, AdversaryResult] = {ar.finding_title: ar for ar in adversary_results}

    scored: list[ScoredFinding] = []

    # Severity normalization — reviewer LLMs sometimes emit uppercase or aliases
    # like "high"/"medium". Map them to the canonical lowercase rubric so downstream
    # code (emoji lookup, by_severity counting, severity_rank gates) doesn't break.
    aliases = {
        "critical": "critical",
        "high": "critical",
        "blocker": "critical",
        "important": "important",
        "medium": "important",
        "major": "important",
        "suggestion": "suggestion",
        "minor": "suggestion",
        "low": "suggestion",
        "nitpick": "nitpick",
        "info": "nitpick",
        "trivia": "nitpick",
        "trivial": "nitpick",
    }

    def _norm_sev(s: str) -> str:
        return aliases.get((s or "").strip().lower(), "suggestion")

    for finding in findings:
        norm_sev = _norm_sev(finding.severity)
        # Base weight from severity
        base = config.base_weights.get(norm_sev, 0.3)

        # Confidence-weighted base
        score = base * finding.confidence

        # Collect active multipliers
        active_multipliers: list[str] = []

        # Adversary assessment
        adversary = adversary_by_title.get(finding.title)
        if adversary:
            if adversary.verdict == "confirmed":
                score *= config.multipliers.get("adversary_confirmed", 1.3)
                active_multipliers.append("adversary_confirmed")
            elif adversary.verdict == "challenged":
                score *= config.multipliers.get("adversary_challenged", 0.5)
                active_multipliers.append("adversary_challenged")

        # AI-generated PR multiplier
        if ai_generated > 0.5:
            score *= config.multipliers.get("ai_generated_pr", 1.2)
            active_multipliers.append("ai_generated_pr")

        # Blast radius multiplier
        if blast_radius_size > 10:
            score *= config.multipliers.get("blast_radius_high", 1.2)
            active_multipliers.append("blast_radius_high")

        # Confidence threshold filtering
        min_confidence = config.confidence_thresholds.get(norm_sev, 0.5)
        if finding.confidence < min_confidence:
            continue  # Drop low-confidence findings

        scored.append(
            ScoredFinding(
                id=f"f_{len(scored):03d}",
                dimension_id=finding.dimension_id,
                dimension_name=finding.dimension_name,
                file_path=finding.file_path,
                line_start=finding.line_start,
                line_end=finding.line_end,
                severity=norm_sev,
                title=finding.title,
                body=finding.body,
                suggestion=finding.suggestion,
                evidence=finding.evidence,
                confidence=finding.confidence,
                tags=finding.tags,
                score=round(score, 3),
                active_multipliers=active_multipliers,
            )
        )

    # Add hidden traps from adversary as new findings
    for ar in adversary_results:
        if ar.verdict == "missed_trap" and ar.hidden_trap:
            scored.append(
                ScoredFinding(
                    id=f"f_{len(scored):03d}",
                    dimension_id="adversary",
                    dimension_name="Adversary Reviewer",
                    file_path="",  # Adversary findings may not have specific lines
                    line_start=0,
                    line_end=0,
                    severity="important",
                    title=f"Hidden trap: {ar.finding_title}",
                    body=ar.hidden_trap,
                    confidence=0.7,
                    tags=["hidden-trap", "adversary-found"],
                    score=round(0.7 * 0.7, 3),  # important × 0.7 confidence
                    active_multipliers=[],
                )
            )

    # Sort by score descending
    scored.sort(key=lambda f: f.score, reverse=True)

    return scored


def determine_review_event(findings: list[ScoredFinding]) -> str:
    """Determine the GitHub review event based on the merge-gate verdict.

    Decoupled from severity. The merge-gate is the single source of truth
    for "must fix before merging". Severity remains the reviewer's badness
    label and drives sorting/display, not the event.

    Returns: APPROVE | COMMENT | REQUEST_CHANGES
    """
    if any(f.blocking for f in findings):
        return "REQUEST_CHANGES"
    if findings:
        return "COMMENT"  # Advisory-only findings: surface, but don't gate merge.
    return "APPROVE"


def deduplicate_exact(findings: list[ReviewFinding]) -> list[ReviewFinding]:
    """Remove exact duplicates: same file + same line range + same severity.

    This is CODE, not LLM. For near-duplicates, use the DedupGate .ai() call.
    """
    seen: set[tuple[str, int, int, str]] = set()
    deduped: list[ReviewFinding] = []

    for finding in findings:
        key = (
            finding.file_path,
            finding.line_start,
            finding.line_end,
            finding.severity,
        )
        if key not in seen:
            seen.add(key)
            deduped.append(finding)

    return deduped
