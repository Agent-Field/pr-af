#!/usr/bin/env python3
"""Render the new PR-AF summary card against an existing benchmark JSON.

Useful for design iteration without spinning up the full pipeline. Loads
findings, overlays a synthetic merge-gate verdict (default: classify everything
as advisory; pass `--blocking-ids id1,id2` to mark specific findings blocking),
then renders the markdown that would be posted to GitHub.

Usage:
  python scripts/render_summary_preview.py benchmark/agentfield-254/pr-af-result-sonnet-254.json
  python scripts/render_summary_preview.py <json> --blocking-ids f_002,f_008
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from unittest.mock import MagicMock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from pr_af.config import ReviewConfig  # noqa: E402
from pr_af.schemas.output import ScoredFinding  # noqa: E402


def _load(path: str) -> tuple[list[ScoredFinding], dict, dict]:
    with open(path) as f:
        blob = json.load(f)
    result = blob.get("result", blob)
    findings = [ScoredFinding(**r) for r in result.get("findings", [])]
    return findings, result.get("metadata", {}).get("intake", {}), result.get("metadata", {}).get("plan", {})


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("path")
    p.add_argument("--blocking-ids", default="", help="comma-separated finding ids to mark blocking")
    p.add_argument(
        "--blocking-reasons",
        default="",
        help="comma-separated reason strings (same order as blocking-ids); optional",
    )
    args = p.parse_args()

    findings, intake_dict, _plan_dict = _load(args.path)
    blocking_ids = {x.strip() for x in args.blocking_ids.split(",") if x.strip()}
    reasons = [r.strip() for r in args.blocking_reasons.split("|") if r.strip()]

    decorated: list[ScoredFinding] = []
    blocking_iter = iter(reasons)
    for f in findings:
        is_blocking = f.id in blocking_ids
        reason = ""
        if is_blocking:
            reason = next(blocking_iter, "Synthetic blocker for preview.")
        else:
            reason = "Synthetic non-blocking for preview — code quality / edge case."
        decorated.append(f.model_copy(update={"blocking": is_blocking, "blocking_reason": reason}))

    # Build a stub orchestrator and call _format_summary directly. We bypass
    # __init__ to avoid wiring agent/app/pr_data. Only the formatting helpers
    # are needed.
    from pr_af.orchestrator import ReviewOrchestrator
    from pr_af.schemas.pipeline import IntakeResult, ReviewPlan

    orch = ReviewOrchestrator.__new__(ReviewOrchestrator)
    orch.config = ReviewConfig()
    orch.review_id = "preview-001"
    orch.total_cost_usd = 0.42
    orch.budget_exhausted = False
    orch.cross_ref_count = 2
    orch.adversary_confirmed_count = 8
    orch.adversary_challenged_count = 3
    orch.coverage_iterations = 1
    orch.agent_invocations = 47
    orch.meta_selector_results = []
    orch.started_at = 0.0  # duration computed from time.monotonic() — close enough for preview
    orch.input = MagicMock(pr_url="https://github.com/example/repo/pull/1")
    orch.pr_data = None

    # Stub helpers that touch self.pr_data / file system.
    orch._normalize_path = lambda p: p  # type: ignore[attr-defined]

    intake = IntakeResult(
        pr_type=intake_dict.get("pr_type", "feature"),
        complexity=intake_dict.get("complexity", "standard"),
        languages=intake_dict.get("languages", ["go"]),
        areas_touched=intake_dict.get("areas_touched", []),
        risk_signals=intake_dict.get("risk_signals", []),
        ai_generated=intake_dict.get("ai_generated", 0.6),
        review_depth=intake_dict.get("review_depth", "standard"),
        pr_summary=intake_dict.get("pr_summary", "Adds config CRUD API + storage abstraction."),
    )
    plan = ReviewPlan(dimensions=[])

    event = "REQUEST_CHANGES" if any(f.blocking for f in decorated) else ("COMMENT" if decorated else "APPROVE")
    print("=" * 70)
    print(f"  EVENT: {event}")
    print(f"  blocking: {sum(1 for f in decorated if f.blocking)}  advisory: {sum(1 for f in decorated if not f.blocking)}")
    print("=" * 70)
    print()
    print(orch._format_summary(findings=decorated, review_event=event, intake=intake, plan=plan))


if __name__ == "__main__":
    main()
