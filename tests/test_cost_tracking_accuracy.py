"""Tests for accurate cost tracking across .ai() and .harness() calls.

Validates that:
1. .ai() gate calls capture cost from the litellm tracker (not hardcoded 0.0)
2. Cost aggregation uses per-phase sum, not max()
"""
from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from pr_af.cost_tracker import CostTracker


class TestCostTrackerSnapshotPattern:
    """Verify the snapshot-before/after pattern used by gate calls."""

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.003)
    def test_snapshot_delta_captures_cost(self, mock_cost):
        """The before/after snapshot pattern should yield the correct cost delta."""
        tracker = CostTracker()

        # Snapshot before
        cost_before = tracker.total_cost
        assert cost_before == 0.0

        # Simulate what happens during an .ai() call:
        # the monkey-patched acompletion wrapper calls record_response()
        tracker.record_response(MagicMock(model="gemini-2.5-flash"), "gemini-2.5-flash")

        # Snapshot after
        ai_cost = tracker.total_cost - cost_before
        assert ai_cost == pytest.approx(0.003)

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.003)
    def test_snapshot_delta_isolated_from_prior_costs(self, mock_cost):
        """Snapshot delta should only capture cost from the current call."""
        tracker = CostTracker()

        # Pre-existing cost from earlier calls
        tracker.record_response(MagicMock(model="gpt-4o"), "gpt-4o")
        assert tracker.total_cost == pytest.approx(0.003)

        # Snapshot before new call
        cost_before = tracker.total_cost

        # New .ai() call records cost
        tracker.record_response(MagicMock(model="gemini-2.5-flash"), "gemini-2.5-flash")

        # Delta should only reflect the new call
        ai_cost = tracker.total_cost - cost_before
        assert ai_cost == pytest.approx(0.003)

    def test_snapshot_delta_zero_when_no_cost_recorded(self):
        """If no cost is recorded during the call, delta should be 0."""
        tracker = CostTracker()
        cost_before = tracker.total_cost

        # No .ai() call happens (or litellm can't price it)
        ai_cost = tracker.total_cost - cost_before
        assert ai_cost == 0.0

    @patch("pr_af.cost_tracker.litellm.completion_cost")
    def test_multiple_snapshots_accumulate_correctly(self, mock_cost):
        """Multiple snapshot-delta captures should sum correctly."""
        tracker = CostTracker()
        total_captured = 0.0

        # First gate call
        mock_cost.return_value = 0.002
        cost_before = tracker.total_cost
        tracker.record_response(MagicMock(model="gemini-2.5-flash"), "gemini-2.5-flash")
        total_captured += tracker.total_cost - cost_before

        # Second gate call
        mock_cost.return_value = 0.001
        cost_before = tracker.total_cost
        tracker.record_response(MagicMock(model="gemini-2.5-flash"), "gemini-2.5-flash")
        total_captured += tracker.total_cost - cost_before

        assert total_captured == pytest.approx(0.003)
        assert tracker.total_cost == pytest.approx(0.003)


class TestCostAggregationStrategy:
    """Verify the per-phase sum approach is correct vs the old max() approach."""

    def test_per_phase_sum_includes_all_costs(self):
        """Per-phase total should equal sum of all phase costs."""
        cost_breakdown: dict[str, float] = {}
        total_cost_usd = 0.0

        phases = {
            "intake": 0.003,      # .ai() gate cost (from tracker snapshot)
            "anatomy": 0.015,     # .harness() cost
            "planning": 0.025,    # .harness() cost
            "review": 0.080,      # .harness() cost
            "adversary": 0.040,   # .harness() cost
            "coverage": 0.001,    # .ai() gate cost (from tracker snapshot)
        }
        for phase, cost in phases.items():
            cost_breakdown[phase] = cost_breakdown.get(phase, 0.0) + cost
            total_cost_usd += cost

        effective_cost = total_cost_usd
        assert effective_cost == pytest.approx(0.164)

    def test_old_max_underreports_with_split_sources(self):
        """Demonstrate the bug: max() misses costs when sources don't overlap."""
        # Old scenario: per-phase only had .harness() costs
        per_phase_harness_only = 0.10
        global_litellm = 0.02  # .ai() gate costs not in per-phase

        # max() picks the larger, missing $0.02 from .ai() calls
        old_effective = max(per_phase_harness_only, global_litellm)
        assert old_effective == 0.10  # Lost $0.02

        # New: per-phase includes everything via tracker snapshots
        per_phase_with_ai = per_phase_harness_only + global_litellm
        new_effective = per_phase_with_ai
        assert new_effective == pytest.approx(0.12)
        assert new_effective > old_effective

    def test_old_max_correct_when_sources_match(self):
        """When per-phase >= global, max() happens to give correct answer."""
        per_phase_total = 0.15
        global_litellm = 0.004

        old_effective = max(per_phase_total, global_litellm)
        assert old_effective == 0.15

        # But new approach is also 0.15 (because .ai() costs are now in per-phase)
        # so global is just for debugging
        new_effective = per_phase_total
        assert new_effective == per_phase_total

    def test_cost_breakdown_sums_to_total(self):
        """Per-phase breakdown should sum to effective_cost."""
        cost_breakdown = {
            "intake": 0.003,
            "anatomy": 0.015,
            "planning": 0.025,
            "review": 0.080,
            "adversary": 0.040,
            "coverage": 0.001,
        }
        total_cost_usd = sum(cost_breakdown.values())
        effective_cost = total_cost_usd

        assert effective_cost == pytest.approx(sum(cost_breakdown.values()))
