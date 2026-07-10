#!/usr/bin/env python
"""Emit the ReviewResult.model_dump() key structure (per §B.2) as JSON.

The Go marshal-parity test builds an equivalent ReviewResult, marshals it, and
asserts the key set at each level matches these Python-derived keys exactly —
catching any accidental omitempty (dropped key) or stray extra field.

Regenerate:
  PYTHONPATH=/home/abir/gb/pr-af/src <venv>/bin/python \
    go/internal/orch/testdata/gen_result_keys.py
"""
from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, "/home/abir/gb/pr-af/src")

from pr_af.schemas.input import GitHubPRData
from pr_af.schemas.output import (
    GitHubComment,
    GitHubReview,
    ReviewMetadata,
    ReviewResult,
    ReviewSummary,
    ScoredFinding,
)
from pr_af.schemas.pipeline import (
    AnatomyResult,
    DiffStats,
    IntakeResult,
    ReviewPlan,
)


def keys(d: dict) -> list[str]:
    return sorted(d.keys())


def main() -> None:
    finding = ScoredFinding(
        id="f_000",
        dimension_id="d1",
        dimension_name="Dim",
        file_path="src/a.py",
        line_start=1,
        line_end=1,
        severity="important",
        title="t",
        body="b",
    )
    comment = GitHubComment(path="src/a.py", line=1, body="c")
    review = GitHubReview(body="body", event="COMMENT", comments=[comment])
    summary = ReviewSummary(total_findings=1, by_severity={"important": 1})
    intake = IntakeResult(
        pr_type="feature",
        complexity="standard",
        languages=[],
        areas_touched=[],
        risk_signals=[],
        ai_generated=0.0,
        review_depth="standard",
        pr_summary="s",
    )
    anatomy = AnatomyResult(
        files=[],
        clusters=[],
        blast_radius=[],
        dependency_graph={},
        stats=DiffStats(),
        pr_narrative="",
        risk_surfaces=[],
        unrelated_changes=[],
        intent_gaps=[],
        context_notes="",
    )
    plan = ReviewPlan(dimensions=[], cross_ref_hints=[])
    metadata = ReviewMetadata(
        intake=intake.model_dump(),
        anatomy=anatomy.model_dump(),
        plan=plan.model_dump(),
        budget={
            "total_cost_usd": 0.0,
            "cost_breakdown": {},
            "budget_exhausted": False,
            "max_cost_usd": 2.0,
            "max_duration_seconds": 300,
        },
        agent_invocations=1,
        phases_completed=[],
    )
    result = ReviewResult(
        review_id="rev_x",
        pr_url="",
        review=review,
        findings=[finding],
        summary=summary,
        metadata=metadata,
    )
    dump = result.model_dump()
    _ = GitHubPRData  # imported for parity of the module surface

    out = {
        "result": keys(dump),
        "review": keys(dump["review"]),
        "comment": keys(dump["review"]["comments"][0]),
        "finding": keys(dump["findings"][0]),
        "summary": keys(dump["summary"]),
        "metadata": keys(dump["metadata"]),
        "intake": keys(dump["metadata"]["intake"]),
        "anatomy": keys(dump["metadata"]["anatomy"]),
        "plan": keys(dump["metadata"]["plan"]),
        "budget": keys(dump["metadata"]["budget"]),
    }
    path = os.path.join(os.path.dirname(__file__), "result_keys.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2, sort_keys=True)
        f.write("\n")
    print(f"wrote {path}")


if __name__ == "__main__":
    main()
