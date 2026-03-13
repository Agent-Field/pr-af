"""Tests for _staggered_gather and related concurrency config."""

from __future__ import annotations

import asyncio
import time

import pytest

from pr_af.config import BudgetConfig
from pr_af.orchestrator import _staggered_gather


@pytest.mark.asyncio
async def test_staggered_gather_returns_all_results():
    """All coroutine results are collected in order."""

    async def make(i: int) -> int:
        return i

    results = await _staggered_gather([make(i) for i in range(5)], delay=0.01)
    assert results == [0, 1, 2, 3, 4]


@pytest.mark.asyncio
async def test_staggered_gather_introduces_delay():
    """Tasks are launched with measurable spacing."""
    launch_times: list[float] = []

    async def record() -> None:
        launch_times.append(time.monotonic())

    await _staggered_gather([record() for _ in range(3)], delay=0.05)

    assert len(launch_times) == 3
    # Second task should start at least 40ms after the first (allowing jitter)
    assert launch_times[1] - launch_times[0] >= 0.04
    assert launch_times[2] - launch_times[1] >= 0.04


@pytest.mark.asyncio
async def test_staggered_gather_zero_delay_is_immediate():
    """When delay=0 it behaves like asyncio.gather."""

    async def make(i: int) -> int:
        return i

    results = await _staggered_gather([make(i) for i in range(3)], delay=0)
    assert results == [0, 1, 2]


@pytest.mark.asyncio
async def test_staggered_gather_single_coro():
    """Single coroutine works without delay."""

    async def make() -> str:
        return "ok"

    results = await _staggered_gather([make()], delay=1.0)
    assert results == ["ok"]


@pytest.mark.asyncio
async def test_staggered_gather_return_exceptions():
    """Exceptions are captured when return_exceptions=True."""

    async def ok() -> str:
        return "ok"

    async def fail() -> str:
        raise ValueError("boom")

    results = await _staggered_gather(
        [ok(), fail(), ok()], delay=0.01, return_exceptions=True,
    )
    assert results[0] == "ok"
    assert isinstance(results[1], ValueError)
    assert results[2] == "ok"


@pytest.mark.asyncio
async def test_staggered_gather_propagates_exception():
    """Without return_exceptions, first exception propagates."""

    async def fail() -> str:
        raise ValueError("boom")

    with pytest.raises(ValueError, match="boom"):
        await _staggered_gather([fail()], delay=0.01)


def test_budget_config_defaults():
    """Verify the updated concurrency and stagger defaults."""
    config = BudgetConfig()
    assert config.max_concurrent_reviewers == 3
    assert config.stagger_delay_seconds == 2.0
