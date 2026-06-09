"""Tests for env-configurable review budget caps.

Maps to the validation contract for ``_resolve_budget_caps``:

* caller passes no value + env set -> env value is used
* caller passes no value + env unset -> historical defaults (2.0 USD / 300s)
* caller passes an explicit value -> it wins over the env var
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import pytest

from pr_af.app import _resolve_budget_caps


def test_env_overrides_when_no_explicit_value(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PR_AF_MAX_DURATION_SECONDS", "1800")
    monkeypatch.setenv("PR_AF_MAX_COST_USD", "5")
    cost, duration = _resolve_budget_caps(None, None)
    assert duration == 1800
    assert cost == 5.0


def test_defaults_when_env_unset(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PR_AF_MAX_DURATION_SECONDS", raising=False)
    monkeypatch.delenv("PR_AF_MAX_COST_USD", raising=False)
    cost, duration = _resolve_budget_caps(None, None)
    assert duration == 300
    assert cost == 2.0


def test_explicit_value_wins_over_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PR_AF_MAX_DURATION_SECONDS", "1800")
    monkeypatch.setenv("PR_AF_MAX_COST_USD", "5")
    cost, duration = _resolve_budget_caps(7.0, 900)
    assert duration == 900
    assert cost == 7.0
