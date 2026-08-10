"""Tests for evidence-extraction resource caps (#65).

Validation contract:

* at most ``_MAX_IDENTIFIERS_PER_FINDING`` repo-wide grep searches are
  dispatched per finding, no matter how many identifiers the body mentions
* the shared file cache is bounded by bytes, not just entry count, and a
  single pathological file larger than the cap is never cached
"""

from __future__ import annotations

from pathlib import Path

import pytest

import pr_af.evidence as evidence
from pr_af.evidence import extract_evidence_for_findings
from pr_af.schemas.pipeline import ReviewFinding


def _finding(body: str) -> ReviewFinding:
    return ReviewFinding(
        dimension_id="d",
        dimension_name="D",
        file_path="src/a.py",
        line_start=1,
        line_end=1,
        severity="suggestion",
        title="t",
        body=body,
        confidence=0.5,
    )


async def test_identifier_grep_fanout_is_capped(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    searched: list[str] = []

    def fake_find_function_callers(repo_path: str, ident: str, exclude_file: str = "") -> list[str]:
        searched.append(ident)
        return []

    monkeypatch.setattr(evidence, "_find_function_callers", fake_find_function_callers)
    body = " ".join(f"`identifier_number_{i}`" for i in range(20))

    await extract_evidence_for_findings(
        findings=[_finding(body)], repo_path=str(tmp_path), diff_patches={}
    )

    assert 0 < len(searched) <= evidence._MAX_IDENTIFIERS_PER_FINDING


def _fresh_cache(monkeypatch: pytest.MonkeyPatch, max_bytes: int) -> None:
    monkeypatch.setattr(evidence, "_FILE_CACHE", {})
    monkeypatch.setattr(evidence, "_FILE_CACHE_BYTES", 0)
    monkeypatch.setattr(evidence, "_FILE_CACHE_MAX_BYTES", max_bytes)


def test_file_cache_is_byte_bounded(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    _fresh_cache(monkeypatch, max_bytes=100)
    file_a = tmp_path / "a.py"
    file_b = tmp_path / "b.py"
    file_a.write_text("a" * 80)
    file_b.write_text("b" * 80)

    evidence._read_file_lines(str(file_a))
    assert len(evidence._FILE_CACHE) == 1
    assert evidence._FILE_CACHE_BYTES == 80

    evidence._read_file_lines(str(file_b))
    assert len(evidence._FILE_CACHE) == 1
    assert evidence._FILE_CACHE_BYTES == 80


def test_oversized_file_served_uncached(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    _fresh_cache(monkeypatch, max_bytes=100)
    big = tmp_path / "big.py"
    big.write_text("x" * 500)

    lines = evidence._read_file_lines(str(big))

    assert lines == ["x" * 500]
    assert evidence._FILE_CACHE == {}
    assert evidence._FILE_CACHE_BYTES == 0
