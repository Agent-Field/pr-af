"""Budget-exhaustion wording distinguishes the cause.

The wall-clock cap gets an explicit "Review time budget exceeded
(max_duration_seconds=<N>) before <phase>" message; the cost cap keeps the
historical "Budget exhausted before <phase>" wording. The strings are §B.4
contracts — byte-identical to the Go port's ``budgetExhaustedMessage``
(see go/internal/orch/budget_test.go).
"""

from __future__ import annotations

from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import ReviewInput


def _orchestrator() -> ReviewOrchestrator:
    return ReviewOrchestrator(app=object(), input=ReviewInput(), config=None)


def test_duration_cap_message_names_the_time_budget() -> None:
    orch = _orchestrator()
    orch.config.budget.max_duration_seconds = 3600
    orch.started_at -= 4000  # simulate 4000s elapsed > 3600s cap

    assert orch._budget_or_timeout_exhausted("intake")
    assert orch.duration_cap_tripped
    assert (
        orch._budget_exhausted_message("intake")
        == "Review time budget exceeded (max_duration_seconds=3600) before intake"
    )


def test_cost_cap_keeps_budget_exhausted_wording() -> None:
    orch = _orchestrator()
    orch.total_cost_usd = orch.config.budget.max_cost_usd

    assert orch._budget_or_timeout_exhausted("review")
    assert not orch.duration_cap_tripped
    assert orch._budget_exhausted_message("review") == "Budget exhausted before review"
