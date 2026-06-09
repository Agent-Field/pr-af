"""Unit tests for the PR-AF human-in-the-loop review gate.

Each test maps to an item in the plan's validation contract: the on/off switch,
the form shape, the decision-parsing for every action/terminal outcome, and the
watchdog-safe create wrapper.
"""

from __future__ import annotations

import time
from types import SimpleNamespace

import pytest

from pr_af.hitl import (
    ACTION_POST,
    ACTION_REJECT,
    ACTION_RERUN,
    HAX_REVIEW_TEMPLATE,
    approval_webhook_url,
    build_hax_client_from_env,
    build_review_payload,
    clean_intent,
    parse_review_decision,
)
from pr_af.hitl.client import create_hax_form_request_with_timeout
from pr_af.schemas.output import ScoredFinding


def _finding(fid: str, severity: str = "important", line: int = 10) -> ScoredFinding:
    return ScoredFinding(
        id=fid,
        dimension_id="d1",
        dimension_name="dim",
        file_path="src/foo.py",
        line_start=line,
        line_end=line,
        severity=severity,
        title=f"title-{fid}",
        body="body",
    )


class _FakeApp:
    def note(self, *args, **kwargs):  # noqa: D401 - test stub
        pass


# --- on/off switch -------------------------------------------------------


def test_build_hax_client_returns_none_without_api_key(monkeypatch):
    monkeypatch.delenv("HAX_API_KEY", raising=False)
    assert build_hax_client_from_env() is None


def test_build_hax_client_returns_client_with_api_key(monkeypatch):
    monkeypatch.setenv("HAX_API_KEY", "test-key")
    monkeypatch.setenv("HAX_SDK_URL", "http://hax.example")
    client = build_hax_client_from_env()
    assert client is not None


# --- payload shape -------------------------------------------------------


def test_payload_has_one_finding_entry_per_finding_all_default_selected():
    findings = [_finding("f1"), _finding("f2", severity="nitpick"), _finding("f3")]
    payload = build_review_payload(pr_intent="adds caching", findings=findings, title="t")

    assert [f["id"] for f in payload["findings"]] == ["f1", "f2", "f3"]
    # All findings pre-checked by default so "post selected" posts everything.
    assert all(f["defaultSelected"] is True for f in payload["findings"])
    # Severity + location are carried through for the rich UI.
    assert payload["findings"][0]["severity"] == "important"
    assert payload["findings"][0]["filePath"] == "src/foo.py"
    assert payload["findings"][0]["lineStart"] == 10
    assert payload["intent"] == "adds caching"


def test_payload_carries_pr_meta_and_revision():
    payload = build_review_payload(
        pr_intent="adds caching",
        findings=[_finding("f1")],
        title="t",
        pr_meta={"repo": "o/r", "number": 7, "author": "", "url": ""},
        revision_iter=1,
        revision_history=["tone it down", ""],
    )
    # Empty meta values are dropped so optional zod fields stay absent.
    assert payload["pr"] == {"repo": "o/r", "number": 7}
    assert payload["revision"]["iteration"] == 1
    assert payload["revision"]["priorInstructions"] == ["tone it down"]


def test_payload_handles_no_findings():
    payload = build_review_payload(pr_intent="docs only", findings=[], title="t")
    assert payload["findings"] == []
    assert "no findings" in payload["reviewSummary"]


def test_clean_intent_strips_html_and_truncates():
    raw = "<details><summary>Release notes</summary><p>Bumps <a href='x'>pkg</a></p></details>"
    cleaned = clean_intent(raw)
    assert "<" not in cleaned and ">" not in cleaned
    assert "Release notes" in cleaned
    assert clean_intent("x" * 5000) .endswith("…")
    assert len(clean_intent("x" * 5000)) <= 701  # max_chars + ellipsis


# --- decision parsing ----------------------------------------------------


def _approval(decision="approved", values=None, feedback=""):
    return SimpleNamespace(
        decision=decision,
        feedback=feedback,
        raw_response={"values": values} if values is not None else None,
    )


def test_post_selected_keeps_only_checked_subset():
    res = _approval(values={"action": ACTION_POST, "findings_to_post": ["f1", "f3"]})
    decision = parse_review_decision(res, ["f1", "f2", "f3"])
    assert decision.is_post
    assert decision.selected_finding_ids == {"f1", "f3"}


def test_post_selected_defaults_to_all_when_field_absent():
    res = _approval(values={"action": ACTION_POST})
    decision = parse_review_decision(res, ["f1", "f2"])
    assert decision.is_post
    assert decision.selected_finding_ids == {"f1", "f2"}


def test_rerun_carries_instructions():
    res = _approval(values={"action": ACTION_RERUN, "instructions": "tone it down"})
    decision = parse_review_decision(res, ["f1"])
    assert decision.is_rerun
    assert decision.instructions == "tone it down"


def test_reject_action_maps_to_reject():
    res = _approval(values={"action": ACTION_REJECT})
    decision = parse_review_decision(res, ["f1"])
    assert decision.is_reject


@pytest.mark.parametrize("terminal", ["expired", "error"])
def test_terminal_decisions_map_to_reject(terminal):
    decision = parse_review_decision(_approval(decision=terminal), ["f1"])
    assert decision.is_reject
    assert decision.decision_raw == terminal


def test_hax_level_reject_without_values_is_reject():
    decision = parse_review_decision(_approval(decision="rejected"), ["f1"])
    assert decision.is_reject


def test_values_nested_under_response_key_are_found():
    res = SimpleNamespace(
        decision="approved",
        feedback="",
        raw_response={"response": {"values": {"action": ACTION_POST, "findings_to_post": ["f2"]}}},
    )
    decision = parse_review_decision(res, ["f1", "f2"])
    assert decision.selected_finding_ids == {"f2"}


# --- callback URL resolution ---------------------------------------------
#
# Contract: the approval callback is invoked by hax-sdk (a separate Railway
# project), so a public URL must win over the internal AGENTFIELD_SERVER the
# agent uses to reach the CP. Conflating them made answered reviews
# undeliverable (the webhook "fetch failed" on an internal hostname).


def test_webhook_url_prefers_public_env_over_internal_server(monkeypatch):
    monkeypatch.setenv("AGENTFIELD_PUBLIC_URL", "https://cp.public.example")
    monkeypatch.setenv("AGENTFIELD_SERVER", "http://control-plane.railway.internal:8080")
    app = SimpleNamespace(agentfield_server="http://control-plane.railway.internal:8080")
    assert (
        approval_webhook_url(app)
        == "https://cp.public.example/api/v1/webhooks/approval-response"
    )


def test_webhook_url_falls_back_to_server_when_no_public(monkeypatch):
    monkeypatch.delenv("AGENTFIELD_PUBLIC_URL", raising=False)
    monkeypatch.delenv("AGENTFIELD_SERVER", raising=False)
    app = SimpleNamespace(agentfield_server="http://cp.local:8080")
    assert approval_webhook_url(app) == "http://cp.local:8080/api/v1/webhooks/approval-response"


def test_webhook_url_uses_server_env_when_app_attr_missing(monkeypatch):
    monkeypatch.delenv("AGENTFIELD_PUBLIC_URL", raising=False)
    monkeypatch.setenv("AGENTFIELD_SERVER", "http://cp.env:8080")
    app = SimpleNamespace(agentfield_server=None)
    assert approval_webhook_url(app) == "http://cp.env:8080/api/v1/webhooks/approval-response"


def test_webhook_url_none_when_nothing_configured(monkeypatch):
    monkeypatch.delenv("AGENTFIELD_PUBLIC_URL", raising=False)
    monkeypatch.delenv("AGENTFIELD_SERVER", raising=False)
    app = SimpleNamespace(agentfield_server=None)
    assert approval_webhook_url(app) is None


# --- watchdog safety -----------------------------------------------------


async def test_create_request_fails_fast_when_hax_wedges():
    """A hung sync create_request must raise quickly, not burn the budget."""

    class _WedgedHax:
        def create_request(self, **kwargs):
            time.sleep(5)  # simulate a wedged hax-sdk call

    start = time.monotonic()
    with pytest.raises(RuntimeError, match="wedged"):
        await create_hax_form_request_with_timeout(
            app=_FakeApp(),
            hax_client=_WedgedHax(),
            payload={"findings": []},
            request_type=HAX_REVIEW_TEMPLATE,
            title="t",
            description=None,
            expires_in_seconds=3600,
            user_id=None,
            webhook_url=None,
            metadata=None,
            timeout_seconds=0.2,
        )
    assert time.monotonic() - start < 3.0  # failed fast, well under the 5s sleep
