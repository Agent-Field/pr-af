"""Tests for the litellm-callback-based cost tracker."""

from __future__ import annotations

import asyncio
import threading
from unittest.mock import MagicMock, patch

import pytest

from pr_af.cost_tracker import CostTracker, get_tracker


class TestCostTracker:
    def _make_response(self, model: str = "test-model") -> MagicMock:
        resp = MagicMock()
        resp.model = model
        return resp

    def test_initial_state(self):
        tracker = CostTracker()
        assert tracker.total_cost == 0.0
        assert tracker.cost_by_model == {}

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.0012)
    def test_log_success_accumulates(self, mock_cost):
        tracker = CostTracker()
        resp = self._make_response("gpt-4")

        tracker.log_success_event({}, resp, None, None)
        assert tracker.total_cost == pytest.approx(0.0012)
        assert tracker.cost_by_model == {"gpt-4": pytest.approx(0.0012)}

        tracker.log_success_event({}, resp, None, None)
        assert tracker.total_cost == pytest.approx(0.0024)

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.005)
    def test_async_log_success(self, mock_cost):
        tracker = CostTracker()
        resp = self._make_response("claude-3")

        asyncio.get_event_loop().run_until_complete(
            tracker.async_log_success_event({}, resp, None, None)
        )
        assert tracker.total_cost == pytest.approx(0.005)

    def test_reset(self):
        tracker = CostTracker()
        with patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.01):
            tracker.log_success_event({}, self._make_response(), None, None)

        assert tracker.total_cost > 0
        tracker.reset()
        assert tracker.total_cost == 0.0
        assert tracker.cost_by_model == {}

    def test_snapshot_and_reset(self):
        tracker = CostTracker()
        with patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.03):
            tracker.log_success_event({}, self._make_response(), None, None)

        val = tracker.snapshot_and_reset()
        assert val == pytest.approx(0.03)
        assert tracker.total_cost == 0.0

    @patch("pr_af.cost_tracker.litellm.completion_cost", side_effect=Exception("unknown model"))
    def test_unknown_model_pricing_skipped(self, mock_cost):
        tracker = CostTracker()
        tracker.log_success_event({}, self._make_response(), None, None)
        assert tracker.total_cost == 0.0

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.0)
    def test_zero_cost_skipped(self, mock_cost):
        tracker = CostTracker()
        tracker.log_success_event({}, self._make_response(), None, None)
        assert tracker.total_cost == 0.0

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.001)
    def test_multiple_models_tracked_separately(self, mock_cost):
        tracker = CostTracker()
        tracker.log_success_event({}, self._make_response("model-a"), None, None)
        tracker.log_success_event({}, self._make_response("model-b"), None, None)
        tracker.log_success_event({}, self._make_response("model-a"), None, None)

        by_model = tracker.cost_by_model
        assert by_model["model-a"] == pytest.approx(0.002)
        assert by_model["model-b"] == pytest.approx(0.001)
        assert tracker.total_cost == pytest.approx(0.003)

    @patch("pr_af.cost_tracker.litellm.completion_cost", return_value=0.001)
    def test_thread_safety(self, mock_cost):
        tracker = CostTracker()
        n_threads = 10
        calls_per_thread = 100

        def worker():
            for _ in range(calls_per_thread):
                tracker.log_success_event({}, self._make_response(), None, None)

        threads = [threading.Thread(target=worker) for _ in range(n_threads)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        expected = n_threads * calls_per_thread * 0.001
        assert tracker.total_cost == pytest.approx(expected, rel=1e-6)


class TestGetTracker:
    def test_returns_singleton(self):
        # get_tracker returns the same instance each time
        t1 = get_tracker()
        t2 = get_tracker()
        assert t1 is t2

    def test_tracker_registered_in_litellm(self):
        import litellm
        tracker = get_tracker()
        assert tracker in litellm.callbacks
