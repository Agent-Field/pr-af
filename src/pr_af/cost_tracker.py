"""Global cost tracking via litellm callbacks.

Intercepts all litellm completion calls in-process and accumulates cost using
``litellm.completion_cost()``.  This bypasses the agentfield SDK's gap where
``.ai()`` discards the response object (and its usage data) before pr-af can
read it.

Note: ``.harness()`` calls that spawn a subprocess (OpenCode CLI) do NOT go
through litellm in this process — they won't be captured here.  Cost for those
must come from the provider or be estimated separately.
"""

from __future__ import annotations

import threading
from typing import Any

import litellm
from litellm.integrations.custom_logger import CustomLogger


class CostTracker(CustomLogger):
    """Accumulates LLM cost from every litellm call in the process."""

    def __init__(self) -> None:
        super().__init__()
        self._lock = threading.Lock()
        self._total: float = 0.0
        self._by_model: dict[str, float] = {}

    # -- public API ----------------------------------------------------------

    @property
    def total_cost(self) -> float:
        with self._lock:
            return self._total

    @property
    def cost_by_model(self) -> dict[str, float]:
        with self._lock:
            return dict(self._by_model)

    def reset(self) -> None:
        with self._lock:
            self._total = 0.0
            self._by_model.clear()

    def snapshot_and_reset(self) -> float:
        """Return accumulated cost and reset the counter (useful between reviews)."""
        with self._lock:
            total = self._total
            self._total = 0.0
            self._by_model.clear()
            return total

    # -- litellm callback interface ------------------------------------------

    def log_success_event(self, kwargs: dict, response_obj: Any, start_time: Any, end_time: Any) -> None:
        self._record(response_obj, kwargs)

    async def async_log_success_event(self, kwargs: dict, response_obj: Any, start_time: Any, end_time: Any) -> None:
        self._record(response_obj, kwargs)

    def _record(self, response_obj: Any, kwargs: dict) -> None:
        try:
            cost = litellm.completion_cost(completion_response=response_obj)
        except Exception:
            # Unknown model pricing or missing usage — silently skip
            return
        if not cost or cost <= 0:
            return
        model = getattr(response_obj, "model", None) or kwargs.get("model", "unknown")
        with self._lock:
            self._total += cost
            self._by_model[model] = self._by_model.get(model, 0.0) + cost


# Module-level singleton — imported by app.py and orchestrator.py
_tracker: CostTracker | None = None


def get_tracker() -> CostTracker:
    """Return the global CostTracker, creating it on first call."""
    global _tracker
    if _tracker is None:
        _tracker = CostTracker()
        litellm.callbacks.append(_tracker)  # type: ignore[arg-type]
    return _tracker
