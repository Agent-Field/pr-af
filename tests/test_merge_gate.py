"""Unit tests for the merge-gate machinery — verdict parsing and event mapping.

These tests do NOT hit OpenRouter. They cover the deterministic parts:
- `_parse_verdict` tolerance to messy model output (markdown fences, leading prose).
- `determine_review_event` correctness across blocking/advisory combinations.
- `classify_findings` integration with a stubbed `app.ai`.
"""

from __future__ import annotations

import asyncio
from types import SimpleNamespace

import pytest

from pr_af.merge_gate import (
    MergeGateVerdict,
    _parse_verdict,
    classify_findings,
)
from pr_af.schemas.output import ScoredFinding
from pr_af.scoring import determine_review_event


def _f(id_: str, *, blocking: bool = False, severity: str = "important") -> ScoredFinding:
    return ScoredFinding(
        id=id_,
        dimension_id="d",
        dimension_name="D",
        file_path="a.go",
        line_start=1,
        line_end=1,
        severity=severity,
        title=f"t-{id_}",
        body="body",
        blocking=blocking,
    )


class TestParseVerdict:
    def test_plain_json(self) -> None:
        v = _parse_verdict('{"blocking": true, "reason": "build break"}')
        assert v.blocking is True
        assert v.reason == "build break"

    def test_with_markdown_fence(self) -> None:
        v = _parse_verdict('```json\n{"blocking": false, "reason": "style"}\n```')
        assert v.blocking is False
        assert v.reason == "style"

    def test_with_leading_prose(self) -> None:
        v = _parse_verdict('Sure, here is the verdict: {"blocking": true, "reason": "x"} done.')
        assert v.blocking is True

    def test_garbage_defaults_to_advisory(self) -> None:
        # On parse failure, advisory is the safe default — we'd rather under-block.
        v = _parse_verdict("model rambled but never closed JSON")
        assert v.blocking is False
        assert "parse" in v.reason.lower()

    def test_non_object_json_defaults_to_advisory(self) -> None:
        v = _parse_verdict('"some string"')
        assert v.blocking is False

    def test_reason_truncated_at_400(self) -> None:
        long = "x" * 1000
        v = _parse_verdict(f'{{"blocking": false, "reason": "{long}"}}')
        assert len(v.reason) <= 400


class TestDetermineReviewEvent:
    def test_empty(self) -> None:
        assert determine_review_event([]) == "APPROVE"

    def test_only_advisory(self) -> None:
        assert determine_review_event([_f("1"), _f("2", severity="critical")]) == "COMMENT"

    def test_any_blocking(self) -> None:
        assert (
            determine_review_event([_f("1"), _f("2", blocking=True, severity="suggestion")])
            == "REQUEST_CHANGES"
        )

    def test_severity_alone_does_not_block(self) -> None:
        # A critical-severity finding that the gate ruled advisory must not block.
        assert determine_review_event([_f("1", severity="critical", blocking=False)]) == "COMMENT"


class _StubAI:
    """Stub `app.ai` that returns canned verdicts keyed by finding title."""

    def __init__(self, verdicts: dict[str, MergeGateVerdict]) -> None:
        self.verdicts = verdicts
        self.calls: list[str] = []

    async def __call__(self, prompt: str, **kwargs: object) -> SimpleNamespace:
        self.calls.append(prompt)
        for title, verdict in self.verdicts.items():
            if title in prompt:
                return SimpleNamespace(text=verdict.model_dump_json())
        return SimpleNamespace(text='{"blocking": false, "reason": "default"}')


class TestClassifyFindings:
    def test_empty_passthrough(self) -> None:
        result = asyncio.run(classify_findings(object(), []))
        assert result == []

    def test_each_finding_classified(self) -> None:
        f1 = _f("1", severity="critical")
        f2 = _f("2", severity="important")
        stub = _StubAI({
            "t-1": MergeGateVerdict(blocking=True, reason="ship blocker"),
            "t-2": MergeGateVerdict(blocking=False, reason="advisory only"),
        })
        app = SimpleNamespace(ai=stub)
        out = asyncio.run(classify_findings(app, [f1, f2]))
        assert len(out) == 2
        assert out[0].blocking is True
        assert out[0].blocking_reason == "ship blocker"
        assert out[1].blocking is False
        assert out[1].blocking_reason == "advisory only"
        # Each finding produces one .ai() call. No de-dup.
        assert len(stub.calls) == 2

    def test_failure_defaults_to_advisory(self) -> None:
        async def boom(*_a: object, **_kw: object) -> SimpleNamespace:
            raise RuntimeError("upstream blew up")

        app = SimpleNamespace(ai=boom)
        f = _f("1", severity="critical")
        out = asyncio.run(classify_findings(app, [f]))
        # Safe default: never block on infra failure.
        assert out[0].blocking is False
        assert "error" in out[0].blocking_reason


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
