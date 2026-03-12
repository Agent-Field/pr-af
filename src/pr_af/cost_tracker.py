"""Global cost tracking via litellm callbacks.

Intercepts all litellm completion calls in-process and accumulates cost using
``litellm.completion_cost()``.  This bypasses the agentfield SDK's gap where
``.ai()`` discards the response object (and its usage data) before pr-af can
read it.

Note: ``.harness()`` calls that spawn a subprocess (OpenCode CLI) do NOT go
through litellm in this process — they won't be captured here.  Cost for those
must come from the provider or be estimated separately.

Implementation note: litellm fires ``async_log_success_event`` as a background
task for ``acompletion()`` calls, so the cost is not available synchronously
via the CustomLogger interface.  To fix this, we monkey-patch
``litellm.acompletion`` to extract cost inline from the response object after
each call completes.
"""

from __future__ import annotations

import functools
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

    # -- inline cost extraction (called synchronously after each response) ---

    def record_response(self, response_obj: Any, model_hint: str = "unknown") -> None:
        """Extract and record cost from a litellm response object.

        Called synchronously right after acompletion/completion returns,
        so cost is available immediately without waiting for async callbacks.
        """
        # OpenRouter strips the "openrouter/" prefix from response.model,
        # so litellm can't identify the provider for pricing.  We pass
        # model_hint (the original kwarg with prefix) to completion_cost().
        cost = 0.0
        try:
            cost = litellm.completion_cost(
                completion_response=response_obj, model=model_hint
            )
        except Exception:
            # Fallback: try without model override (works for non-OpenRouter)
            try:
                cost = litellm.completion_cost(completion_response=response_obj)
            except Exception:
                return
        if not cost or cost <= 0:
            return
        model = getattr(response_obj, "model", None) or model_hint
        with self._lock:
            self._total += cost
            self._by_model[model] = self._by_model.get(model, 0.0) + cost

    # -- litellm callback interface (kept as fallback for sync completion) ---

    def log_success_event(self, kwargs: dict, response_obj: Any, start_time: Any, end_time: Any) -> None:
        self.record_response(response_obj, kwargs.get("model", "unknown"))

    async def async_log_success_event(self, kwargs: dict, response_obj: Any, start_time: Any, end_time: Any) -> None:
        # No-op: we capture cost inline via the acompletion wrapper instead,
        # so this avoids double-counting.
        pass


# Module-level singleton — imported by app.py and orchestrator.py
_tracker: CostTracker | None = None
_patched = False


def _install_acompletion_wrapper(tracker: CostTracker) -> None:
    """Wrap ``litellm.acompletion`` to record cost synchronously after each call."""
    global _patched
    if _patched:
        return
    _patched = True

    _original_acompletion = litellm.acompletion

    @functools.wraps(_original_acompletion)
    async def _tracked_acompletion(*args: Any, **kwargs: Any) -> Any:
        response = await _original_acompletion(*args, **kwargs)
        tracker.record_response(response, kwargs.get("model", "unknown"))
        return response

    litellm.acompletion = _tracked_acompletion  # type: ignore[assignment]


def get_tracker() -> CostTracker:
    """Return the global CostTracker, creating it on first call."""
    global _tracker
    if _tracker is None:
        _tracker = CostTracker()
        # Keep the callback for sync litellm.completion() calls
        litellm.callbacks.append(_tracker)  # type: ignore[arg-type]
        # Wrap acompletion for immediate cost capture on async calls
        _install_acompletion_wrapper(_tracker)
    return _tracker
