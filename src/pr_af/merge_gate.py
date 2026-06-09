"""Merge-blocker gate — parallel `.ai()` pass over scored findings.

A separate lens from severity. Severity asks "how bad is this issue?".
The merge gate asks "must this be fixed BEFORE the PR ships, or can it
ship and be addressed in a follow-up?". A pedantic `critical` finding
(e.g. a wrong test mock signature in unreachable code) can still be
non-blocking. A subtle `important` regression on a hot path can be
blocking.

Production goal: keep automated review useful without forcing a
REQUEST_CHANGES gate on every alarmist finding. Only true must-fix
issues should block merge. Everything else stays as advisory comments.

Architecture:
- One `.ai()` per finding, fired in parallel (mirror of polish.py).
- Failure mode: default to `blocking=False`. False negatives (a real
  blocker passes through as advisory) are recoverable by human review.
  False positives (advisory issue flagged blocking) erode trust and
  block merge — worse outcome.
"""

from __future__ import annotations

import asyncio
import json
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from .schemas.output import ScoredFinding


_MERGE_GATE_SYSTEM = (
    "You are the release manager for an automated code reviewer. Your job is to "
    "decide whether a single review finding must be fixed BEFORE this PR is merged, "
    "or whether the team can safely merge now and address it later.\n"
    "\n"
    "Apply a TIGHT bar. Only call something `blocking` if at least one is true:\n"
    "  - It breaks the build, tests, or type-checking.\n"
    "  - It introduces a security vulnerability reachable from a real user-facing "
    "code path (auth bypass, injection, credential leak, RCE, exposed secret, "
    "missing access control on a route real clients hit).\n"
    "  - It causes data loss, data corruption, or irreversible state damage in "
    "production-running code.\n"
    "  - It breaks an existing public API/CLI/schema contract that real callers "
    "depend on, with no migration path.\n"
    "  - It is a regression of behavior that was working before this PR.\n"
    "\n"
    "Treat the following as NON-blocking (return blocking=false):\n"
    "  - Code quality, style, naming, refactor opportunities.\n"
    "  - Missing tests for edge cases, low test coverage, mock signature drift in "
    "test helpers.\n"
    "  - Defensive programming opportunities, missing input validation that has "
    "no demonstrated reachable exploit path.\n"
    "  - Performance suggestions that don't change correctness.\n"
    "  - Documentation, comments, README, type-hint completeness.\n"
    "  - 'Should also handle X' suggestions when X isn't currently reachable.\n"
    "  - Architectural critiques (DRY, single source of truth, layering) without a "
    "concrete production impact described in the finding.\n"
    "  - Issues whose reachability or exploitability the finding itself cannot "
    "demonstrate concretely.\n"
    "\n"
    "If the finding's evidence does NOT concretely demonstrate one of the blocking "
    "criteria above — even when the severity is labeled 'critical' — return "
    "blocking=false. Reviewers are often alarmist; you are the calibrating layer.\n"
    "\n"
    "Output strict JSON with this exact shape and nothing else:\n"
    '  {"blocking": true | false, "reason": "<one short sentence>"}\n'
    "Do not add prose. Do not wrap in markdown fences. JSON only."
)


class MergeGateVerdict(BaseModel):
    """Per-finding gate output."""

    blocking: bool = False
    reason: str = Field(default="", max_length=400)


def _build_user_prompt(finding: ScoredFinding) -> str:
    parts = [
        "# Finding\n",
        f"Severity (reviewer's label): {finding.severity}\n",
        f"Confidence: {finding.confidence:.2f}\n",
        f"File: {finding.file_path}:{finding.line_start}\n",
        f"Title: {finding.title}\n",
        f"\n## Body\n{finding.body}\n",
    ]
    if finding.evidence:
        parts.append(f"\n## Evidence\n{finding.evidence}\n")
    if finding.suggestion:
        parts.append(f"\n## Suggested fix\n{finding.suggestion}\n")
    parts.append(
        "\n# Question\n"
        "Must this be fixed before this PR is merged to production? "
        "Apply the bar described in the system prompt. Reply with JSON only."
    )
    return "".join(parts)


def _parse_verdict(raw: str) -> MergeGateVerdict:
    """Tolerant JSON parsing — strips markdown fences, picks the first JSON object."""

    text = raw.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:].strip()
    # Find first `{...}` block
    start = text.find("{")
    end = text.rfind("}")
    if start >= 0 and end > start:
        text = text[start : end + 1]
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return MergeGateVerdict(blocking=False, reason="gate parse error")
    if not isinstance(data, dict):
        return MergeGateVerdict(blocking=False, reason="gate non-object")
    return MergeGateVerdict(
        blocking=bool(data.get("blocking", False)),
        reason=str(data.get("reason", ""))[:400],
    )


async def _gate_one(app: Any, finding: ScoredFinding) -> MergeGateVerdict:
    try:
        # response_format="json" forces JSON output so reasoning models don't
        # leak their chain-of-thought before the verdict and truncate the answer.
        out = await app.ai(
            _build_user_prompt(finding),
            system=_MERGE_GATE_SYSTEM,
            response_format="json",
        )
    except Exception as exc:  # noqa: BLE001
        print(f"[PR-AF] Merge-gate skipped for {finding.id}: {exc.__class__.__name__}", flush=True)
        return MergeGateVerdict(blocking=False, reason="gate error")
    text = getattr(out, "text", None) or getattr(out, "content", None) or str(out)
    return _parse_verdict(text)


async def classify_findings(
    app: Any, findings: list[ScoredFinding]
) -> list[ScoredFinding]:
    """Return a new list of findings with `blocking` and `blocking_reason` populated.

    Original order preserved. Pure function — input is not mutated.
    """

    if not findings:
        return findings
    verdicts = await asyncio.gather(*(_gate_one(app, f) for f in findings))
    classified = [
        f.model_copy(update={"blocking": v.blocking, "blocking_reason": v.reason})
        for f, v in zip(findings, verdicts, strict=True)
    ]
    blocking_count = sum(1 for f in classified if f.blocking)
    print(
        f"[PR-AF] Merge-gate: {blocking_count}/{len(classified)} findings classified blocking",
        flush=True,
    )
    return classified
