#!/usr/bin/env python
"""Generate the byte-exact summary-Markdown golden for the Go orch output test.

Runs the REAL Python ReviewOrchestrator._format_summary against a fixed fixture
with a controlled clock/review_id, writing testdata/summary_golden.txt. The Go
test builds the identical fixture and asserts its formatSummary output matches
these bytes exactly (validation contract V11 / design risk 3: float + Markdown
parity).

Regenerate:
  PYTHONPATH=/home/abir/gb/pr-af/src \
    <venv>/bin/python go/internal/orch/testdata/gen_summary_golden.py
"""
from __future__ import annotations

import os
import sys

sys.path.insert(0, "/home/abir/gb/pr-af/src")

import pr_af.orchestrator as orch_mod
from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import ReviewInput
from pr_af.schemas.output import ScoredFinding
from pr_af.schemas.pipeline import (
    IntakeResult,
    MetaDimensionResult,
    ReviewDimension,
    ReviewPlan,
)


def _dim(did: str, name: str, targets: list[str]) -> ReviewDimension:
    return ReviewDimension(id=did, name=name, review_prompt="", target_files=targets)


def main() -> None:
    orch = ReviewOrchestrator(app=object(), input=ReviewInput())
    orch.review_id = "rev_abc123def456"
    orch.cross_ref_count = 1
    orch.adversary_confirmed_count = 2
    orch.adversary_challenged_count = 1
    orch.coverage_iterations = 1
    orch.agent_invocations = 7
    orch.total_cost_usd = 0.0
    orch.budget_exhausted = False
    orch.meta_selector_results = [
        MetaDimensionResult(
            lens="semantic",
            dimensions=[_dim("s1", "A", []), _dim("s2", "B", [])],
            confidence=0.8,
        ),
        MetaDimensionResult(lens="mechanical", dimensions=[_dim("m1", "C", [])], confidence=0.6),
    ]

    # Controlled clock: started_at 0.0, time.monotonic() == 12.3 → duration 12.3s.
    orch.started_at = 0.0
    orch_mod.time.monotonic = lambda: 12.3

    findings = [
        ScoredFinding(
            id="f_000",
            dimension_id="d1",
            dimension_name="Security",
            file_path="src/a.py",
            line_start=10,
            line_end=10,
            severity="critical",
            title="Null dereference",
            body="The value can be None. Handle it before use.",
            confidence=0.9,
            blocking=True,
            blocking_reason="Crashes on null input.",
        ),
        ScoredFinding(
            id="f_001",
            dimension_id="d2",
            dimension_name="Performance",
            file_path="src/b.py",
            line_start=20,
            line_end=25,
            severity="important",
            title="Quadratic loop",
            body="This nested loop is O(n^2) and will not scale.",
            confidence=0.7,
            blocking=False,
        ),
        ScoredFinding(
            id="f_002",
            dimension_id="d3",
            dimension_name="Style",
            file_path="",
            line_start=0,
            line_end=0,
            severity="nitpick",
            title="Typo in comment",
            body="Fix the spelling.",
            confidence=0.5,
            blocking=False,
        ),
    ]
    intake = IntakeResult(
        pr_type="feature",
        complexity="standard",
        languages=["python"],
        areas_touched=["api"],
        risk_signals=[],
        ai_generated=0.1,
        review_depth="standard",
        pr_summary="Adds a caching layer to the API.",
    )
    plan = ReviewPlan(
        dimensions=[
            _dim("sec", "Security", ["src/a.py"]),
            _dim("perf", "Performance", ["src/b.py", "src/c.py"]),
        ],
        cross_ref_hints=[],
    )

    body = orch._format_summary(
        findings=findings, review_event="REQUEST_CHANGES", intake=intake, plan=plan
    )
    out = os.path.join(os.path.dirname(__file__), "summary_golden.txt")
    with open(out, "w", encoding="utf-8") as f:
        f.write(body)
    print(f"wrote {out} ({len(body.encode('utf-8'))} bytes)")


if __name__ == "__main__":
    main()
