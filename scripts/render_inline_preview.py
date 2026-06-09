#!/usr/bin/env python3
"""Render an inline-comment body for visual verification.

Loads N findings from a benchmark JSON, applies a synthetic gate verdict,
and prints the resulting inline-comment bodies (one for blocking, one for
advisory) so we can eyeball GFM alert rendering before posting to a real PR.
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


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("path")
    args = p.parse_args()

    with open(args.path) as f:
        blob = json.load(f)
    result = blob.get("result", blob)
    findings = [ScoredFinding(**r) for r in result.get("findings", [])]

    from pr_af.orchestrator import ReviewOrchestrator

    orch = ReviewOrchestrator.__new__(ReviewOrchestrator)
    orch.config = ReviewConfig()
    orch.input = MagicMock(pr_url="x")
    orch.pr_data = None
    orch._normalize_path = lambda p: p  # type: ignore[attr-defined]

    if not findings:
        print("No findings in input.", file=sys.stderr)
        return

    blocking_f = findings[0].model_copy(update={
        "blocking": True,
        "blocking_reason": (
            "Unauthenticated public route allows config writes that overwrite admin tokens "
            "in the default deployment."
        ),
    })
    advisory_f = findings[1 if len(findings) > 1 else 0].model_copy(update={
        "blocking": False,
        "blocking_reason": (
            "Code quality issue without a demonstrated production code path; safe to address "
            "in follow-up."
        ),
    })

    print("=" * 70)
    print("INLINE COMMENT — BLOCKING (must-fix)")
    print("=" * 70)
    body = orch._format_comment_body(blocking_f)
    print(orch._decorate_with_blocking(blocking_f, body))
    print()
    print("=" * 70)
    print("INLINE COMMENT — ADVISORY (non-blocking)")
    print("=" * 70)
    body = orch._format_comment_body(advisory_f)
    print(orch._decorate_with_blocking(advisory_f, body))


if __name__ == "__main__":
    main()
