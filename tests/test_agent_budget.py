"""Tests for the review-wide agent-concurrency budget (#65).

Validation contract:

* concurrent leaf-agent invocations never exceed the cap at any instant, even
  when the coverage/review path and consistency-verify run concurrently
* the deprecated ``max_concurrent_reviewers`` knob still binds (effective cap
  is the min of both knobs)
* the consistency-verify obligation count is configurable, not a literal
* ``PR_AF_MAX_CONCURRENT_AGENTS`` / ``PR_AF_MAX_CONSISTENCY_OBLIGATIONS`` env
  knobs and the ``max_concurrent_agents`` input field plumb through
"""

from __future__ import annotations

import asyncio

import pytest

import pr_af.orchestrator as orchestrator_module
from pr_af.app import _webhook_review_limits
from pr_af.config import BudgetConfig, ReviewConfig
from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import ChangedFile, GitHubPRData, ReviewInput
from pr_af.schemas.pipeline import ReviewDimension, ReviewPlan


class ConcurrencyProbe:
    """Counts in-flight fake agent calls and records the peak."""

    def __init__(self) -> None:
        self.active = 0
        self.peak = 0
        self.calls = 0

    async def run(self) -> None:
        self.active += 1
        self.calls += 1
        self.peak = max(self.peak, self.active)
        await asyncio.sleep(0.02)
        self.active -= 1


def _make_orchestrator(config: ReviewConfig) -> ReviewOrchestrator:
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="diff"), config=config)
    orchestrator.pr_data = GitHubPRData(
        owner="",
        repo="",
        number=0,
        title="t",
        description="",
        diff="@@ -1 +1 @@\n+x",
        changed_files=[
            ChangedFile(path="src/a.py", status="modified", additions=1, deletions=0, patch="@@ -1 +1 @@\n+x")
        ],
    )
    return orchestrator


def _plan(n: int) -> ReviewPlan:
    return ReviewPlan(
        dimensions=[
            ReviewDimension(id=f"d{i}", name=f"D{i}", review_prompt="p", target_files=["src/a.py"])
            for i in range(n)
        ]
    )


async def _drain(queue: asyncio.Queue) -> None:
    while await queue.get() is not None:
        pass


async def test_shared_budget_caps_leaf_agents_across_concurrent_phases(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config = ReviewConfig()
    config.budget.max_concurrent_agents = 2
    orchestrator = _make_orchestrator(config)
    probe = ConcurrencyProbe()

    async def fake_review_dimension(**kwargs: object) -> dict:
        await probe.run()
        return {"findings": [], "sub_reviews": []}

    async def fake_extract_obligations(**kwargs: object) -> dict:
        await probe.run()
        return {"obligations": [{"id": i} for i in range(4)]}

    async def fake_verify_obligation(**kwargs: object) -> dict:
        await probe.run()
        return {"holds": True}

    monkeypatch.setattr(orchestrator_module, "review_dimension", fake_review_dimension)
    monkeypatch.setattr(orchestrator_module, "extract_obligations", fake_extract_obligations)
    monkeypatch.setattr(orchestrator_module, "verify_obligation", fake_verify_obligation)

    queue: asyncio.Queue = asyncio.Queue()
    await asyncio.wait_for(
        asyncio.gather(
            orchestrator._run_parallel_review(_plan(3), queue),
            orchestrator._run_consistency_verify([]),
            _drain(queue),
        ),
        timeout=5,
    )

    assert probe.calls == 3 + 1 + 4
    assert probe.peak <= 2


async def test_deprecated_reviewer_knob_still_binds(monkeypatch: pytest.MonkeyPatch) -> None:
    config = ReviewConfig()
    config.budget.max_concurrent_agents = 8
    config.budget.max_concurrent_reviewers = 1
    orchestrator = _make_orchestrator(config)
    probe = ConcurrencyProbe()

    async def fake_review_dimension(**kwargs: object) -> dict:
        await probe.run()
        return {"findings": [], "sub_reviews": []}

    monkeypatch.setattr(orchestrator_module, "review_dimension", fake_review_dimension)

    queue: asyncio.Queue = asyncio.Queue()
    await asyncio.wait_for(
        asyncio.gather(orchestrator._run_parallel_review(_plan(3), queue), _drain(queue)),
        timeout=5,
    )

    assert probe.calls == 3
    assert probe.peak == 1


async def test_consistency_obligation_cap_is_configurable(monkeypatch: pytest.MonkeyPatch) -> None:
    config = ReviewConfig()
    config.budget.max_consistency_obligations = 2
    orchestrator = _make_orchestrator(config)
    verify_calls = 0

    async def fake_extract_obligations(**kwargs: object) -> dict:
        return {"obligations": [{"id": i} for i in range(5)]}

    async def fake_verify_obligation(**kwargs: object) -> dict:
        nonlocal verify_calls
        verify_calls += 1
        return {"holds": True}

    monkeypatch.setattr(orchestrator_module, "extract_obligations", fake_extract_obligations)
    monkeypatch.setattr(orchestrator_module, "verify_obligation", fake_verify_obligation)

    await asyncio.wait_for(orchestrator._run_consistency_verify([]), timeout=5)

    assert verify_calls == 2


def test_env_knobs_drive_budget_defaults(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PR_AF_MAX_CONCURRENT_AGENTS", "4")
    monkeypatch.setenv("PR_AF_MAX_CONSISTENCY_OBLIGATIONS", "6")
    budget = BudgetConfig()
    assert budget.max_concurrent_agents == 4
    assert budget.max_consistency_obligations == 6

    monkeypatch.delenv("PR_AF_MAX_CONCURRENT_AGENTS")
    monkeypatch.delenv("PR_AF_MAX_CONSISTENCY_OBLIGATIONS")
    budget = BudgetConfig()
    assert budget.max_concurrent_agents == 8
    assert budget.max_consistency_obligations == 12


def test_input_override_plumbs_through() -> None:
    config = ReviewConfig.from_input(ReviewInput(diff_text="d", max_concurrent_agents=3))
    assert config.budget.max_concurrent_agents == 3


def test_webhook_limits_include_agent_budget(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PR_AF_MAX_CONCURRENT_AGENTS", "2")
    monkeypatch.delenv("PR_AF_MAX_CONCURRENT_REVIEWERS", raising=False)
    monkeypatch.delenv("PR_AF_MAX_REVIEW_DEPTH", raising=False)
    monkeypatch.delenv("PR_AF_MAX_COVERAGE_ITERATIONS", raising=False)
    assert _webhook_review_limits() == {"max_concurrent_agents": 2}
