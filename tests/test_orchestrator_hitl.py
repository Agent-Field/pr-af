"""Orchestrator-level tests for the HITL revision loop.

Maps to the validation contract items about end-to-end gate behavior: HITL off
posts directly; approve-subset posts only the selected findings; rerun re-runs
the review phases with feedback then posts; reject/expire/cap posts nothing.

The review phases and GitHub post are stubbed — these tests exercise the control
flow in ``ReviewOrchestrator.run``, not the LLM pipeline.
"""

from __future__ import annotations

from types import SimpleNamespace

import pr_af.orchestrator as orch_mod
from pr_af.hitl import ACTION_POST, ACTION_REJECT, ACTION_RERUN, ReviewDecision
from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import GitHubPRData, ReviewInput
from pr_af.schemas.output import ScoredFinding


def _finding(fid: str) -> ScoredFinding:
    return ScoredFinding(
        id=fid,
        dimension_id="d1",
        dimension_name="dim",
        file_path="src/foo.py",
        line_start=10,
        line_end=10,
        severity="important",
        title=f"title-{fid}",
        body="body",
    )


class _FakeApp:
    def __init__(self):
        self.notes = []
        self.ctx = SimpleNamespace(execution_id="exec_1")

    def note(self, msg, **kwargs):
        self.notes.append(msg)


def _make_orchestrator(monkeypatch, *, findings, hitl_on, decisions):
    """Build an orchestrator with all heavy phases stubbed.

    ``decisions`` is a list of ReviewDecision returned by successive
    request_review_approval calls.
    """
    inp = ReviewInput(pr_url="https://github.com/o/r/pull/1", dry_run=False)
    orch = ReviewOrchestrator(app=_FakeApp(), input=inp)

    async def fake_intake():
        orch.pr_data = GitHubPRData(owner="o", repo="r", number=1, title="t", description="")
        return SimpleNamespace(pr_summary="adds caching", pr_type="feature", complexity="low")

    async def fake_anatomy(intake):
        return SimpleNamespace(files=[], clusters=[])

    feedbacks: list[str] = []

    async def fake_phases(intake, anatomy, depth, reviewer_feedback=""):
        feedbacks.append(reviewer_feedback)
        return SimpleNamespace(dimensions=[]), list(findings)

    captured: dict = {}

    async def fake_generate(scored, intake, anatomy, plan, *, post_to_github=True):
        captured["findings"] = list(scored)
        captured["post"] = post_to_github
        return SimpleNamespace(
            summary=SimpleNamespace(total_findings=len(scored), cost_usd=0.0)
        )

    monkeypatch.setattr(orch, "_run_intake", fake_intake)
    monkeypatch.setattr(orch, "_run_anatomy", fake_anatomy)
    monkeypatch.setattr(orch, "_resolve_depth", lambda intake: "standard")
    monkeypatch.setattr(orch, "_run_review_phases", fake_phases)
    monkeypatch.setattr(orch, "_generate_output", fake_generate)
    monkeypatch.setattr(orch, "_cleanup_context_dir", lambda: None)

    monkeypatch.setattr(
        orch_mod, "build_hax_client_from_env", lambda: object() if hitl_on else None
    )
    monkeypatch.setattr(orch_mod, "approval_webhook_url", lambda app: None)

    call_count = {"n": 0}

    async def fake_request(**kwargs):
        idx = call_count["n"]
        call_count["n"] += 1
        return decisions[idx]

    monkeypatch.setattr(orch_mod, "request_review_approval", fake_request)

    return orch, captured, feedbacks, call_count


async def test_hitl_off_posts_directly(monkeypatch):
    findings = [_finding("f1"), _finding("f2")]
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch, findings=findings, hitl_on=False, decisions=[]
    )
    await orch.run()
    assert captured["post"] is True
    assert {f.id for f in captured["findings"]} == {"f1", "f2"}
    assert calls["n"] == 0  # gate never consulted


async def test_approve_subset_posts_only_selected(monkeypatch):
    findings = [_finding("f1"), _finding("f2"), _finding("f3")]
    decision = ReviewDecision(action=ACTION_POST, selected_finding_ids={"f1", "f3"})
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch, findings=findings, hitl_on=True, decisions=[decision]
    )
    await orch.run()
    assert captured["post"] is True
    assert {f.id for f in captured["findings"]} == {"f1", "f3"}


async def test_rerun_then_post_threads_feedback(monkeypatch):
    findings = [_finding("f1")]
    decisions = [
        ReviewDecision(action=ACTION_RERUN, instructions="too aggressive, tone it down"),
        ReviewDecision(action=ACTION_POST, selected_finding_ids={"f1"}),
    ]
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch, findings=findings, hitl_on=True, decisions=decisions
    )
    await orch.run()
    assert calls["n"] == 2  # asked twice
    # First phase run had no feedback; the re-run carried the reviewer's words.
    assert feedbacks[0] == ""
    assert "tone it down" in feedbacks[1]
    assert captured["post"] is True


async def test_hitl_on_zero_findings_skips_gate(monkeypatch):
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch, findings=[], hitl_on=True, decisions=[]
    )
    await orch.run()
    assert captured["post"] is False  # nothing posted to a public repo
    assert calls["n"] == 0  # human never bothered for an empty review


async def test_reject_posts_nothing(monkeypatch):
    findings = [_finding("f1")]
    decision = ReviewDecision(action=ACTION_REJECT, decision_raw="rejected")
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch, findings=findings, hitl_on=True, decisions=[decision]
    )
    await orch.run()
    assert captured["post"] is False


async def test_rerun_past_cap_posts_nothing(monkeypatch):
    findings = [_finding("f1")]
    orch, captured, feedbacks, calls = _make_orchestrator(
        monkeypatch,
        findings=findings,
        hitl_on=True,
        decisions=[ReviewDecision(action=ACTION_RERUN, instructions="again")] * 5,
    )
    # Default max_review_revisions = 2 → 3 prompts (iters 0,1,2), then no post.
    orch.config.hitl.max_review_revisions = 2
    await orch.run()
    assert captured["post"] is False
    assert calls["n"] == 3
