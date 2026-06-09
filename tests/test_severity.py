"""Tests for the canonical severity vocabulary and its enforcement.

Validation contract (behavior, not implementation):

* ``normalize_severity`` maps any label to the 4-level canonical scale:
  canonical values are returned unchanged; known synonyms map by rank
  (``high`` → ``important``, ``medium`` → ``suggestion``, ``low`` → ``nitpick``);
  matching is case-insensitive and whitespace-tolerant; empty / non-string /
  unknown input falls back to ``suggestion``.
* Finding models coerce a stray ``severity`` at validation time, so a model that
  emits ``"high"`` never produces an out-of-vocabulary value downstream.
* The HITL payload (``build_review_payload`` → ``_finding_payload``) only ever
  emits canonical severities — this is the contract the hax-sdk template's zod
  enum enforces, and the bug this guards against (a ``"high"`` finding 422'd the
  whole review-approval create).
"""

from __future__ import annotations

import pytest

from pr_af.hitl import build_review_payload
from pr_af.schemas.output import ScoredFinding
from pr_af.schemas.pipeline import ReviewFinding
from pr_af.schemas.severity import VALID_SEVERITIES, normalize_severity

# --- normalize_severity: synonym mapping --------------------------------


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("critical", "critical"),
        ("important", "important"),
        ("suggestion", "suggestion"),
        ("nitpick", "nitpick"),
        # the incident: "high" must land on "important", not blow up
        ("high", "important"),
        ("medium", "suggestion"),
        ("low", "nitpick"),
        ("blocker", "critical"),
        ("error", "important"),
        ("warning", "suggestion"),
        ("info", "nitpick"),
        ("minor", "nitpick"),
    ],
)
def test_normalize_maps_known_labels_by_rank(raw, expected):
    assert normalize_severity(raw) == expected


def test_normalize_is_case_and_whitespace_insensitive():
    assert normalize_severity("  HIGH ") == "important"
    assert normalize_severity("Critical") == "critical"


@pytest.mark.parametrize("bad", ["", "   ", "bogus", "p1", None, 3, ["high"]])
def test_normalize_falls_back_to_default_for_unusable_input(bad):
    assert normalize_severity(bad) == "suggestion"


def test_normalize_respects_explicit_default():
    assert normalize_severity("", default="nitpick") == "nitpick"


def test_normalize_output_is_always_canonical():
    for raw in ["high", "medium", "low", "blocker", "weird", ""]:
        assert normalize_severity(raw) in VALID_SEVERITIES


# --- model-level coercion -----------------------------------------------


def test_review_finding_coerces_stray_severity():
    f = ReviewFinding(
        dimension_id="d",
        dimension_name="dim",
        file_path="a.py",
        line_start=1,
        line_end=1,
        severity="high",
        title="t",
        body="b",
    )
    assert f.severity == "important"


def test_scored_finding_coerces_stray_severity():
    f = ScoredFinding(
        id="f1",
        dimension_id="d",
        dimension_name="dim",
        file_path="a.py",
        line_start=1,
        line_end=1,
        severity="HIGH",
        title="t",
        body="b",
    )
    assert f.severity == "important"


# --- HITL payload boundary ----------------------------------------------


def _scored(severity: str) -> ScoredFinding:
    return ScoredFinding(
        id="f1",
        dimension_id="d",
        dimension_name="dim",
        file_path="a.py",
        line_start=1,
        line_end=1,
        severity=severity,
        title="t",
        body="b",
    )


def test_hitl_payload_severity_is_canonical_even_from_stray_label():
    # Build a finding, then force a stray severity past validation to prove the
    # _finding_payload boundary still normalizes (defense in depth).
    finding = _scored("critical")
    object.__setattr__(finding, "severity", "high")
    payload = build_review_payload(pr_intent="x", findings=[finding], title="t")
    assert payload["findings"][0]["severity"] == "important"
