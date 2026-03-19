"""Per-review runtime configuration via contextvars.

Allows each concurrent review to use a different provider/model without
shared mutable state. Set once at the start of review(), read by every
.harness() and .ai() call via get_harness_kwargs() / get_ai_kwargs().

Usage in review():
    from pr_af.runtime_config import set_runtime_overrides
    set_runtime_overrides(provider="claude-code", harness_model="claude-sonnet-4-6", ai_model="anthropic/claude-sonnet-4-6")

Usage in reasoner functions:
    from pr_af.runtime_config import get_harness_kwargs, get_ai_kwargs
    result = await router.app.harness(prompt, schema=Schema, **get_harness_kwargs())
    gate = await router.app.ai(prompt, schema=Schema, **get_ai_kwargs())
"""

from __future__ import annotations

import contextvars

_provider: contextvars.ContextVar[str | None] = contextvars.ContextVar("pr_af_provider", default=None)
_harness_model: contextvars.ContextVar[str | None] = contextvars.ContextVar("pr_af_harness_model", default=None)
_ai_model: contextvars.ContextVar[str | None] = contextvars.ContextVar("pr_af_ai_model", default=None)


def set_runtime_overrides(
    provider: str | None = None,
    harness_model: str | None = None,
    ai_model: str | None = None,
) -> None:
    """Set per-review overrides. Called once at the start of review()."""
    _provider.set(provider)
    _harness_model.set(harness_model)
    _ai_model.set(ai_model)


def clear_runtime_overrides() -> None:
    """Reset overrides to defaults. Called at the end of review()."""
    _provider.set(None)
    _harness_model.set(None)
    _ai_model.set(None)


def get_harness_kwargs() -> dict[str, str]:
    """Returns provider/model kwargs to spread into .harness() calls.

    When no override is set, returns empty dict (SDK uses its defaults).
    """
    kwargs: dict[str, str] = {}
    p = _provider.get()
    if p is not None:
        kwargs["provider"] = p
    m = _harness_model.get()
    if m is not None:
        kwargs["model"] = m
    return kwargs


def get_ai_kwargs() -> dict[str, object]:
    """Returns model kwargs to spread into .ai() calls.

    When no override is set, returns empty dict (SDK uses its defaults).
    When the model uses anthropic/ prefix, injects ANTHROPIC_API_KEY and
    api_base so litellm routes to Anthropic instead of OpenRouter.
    """
    import os

    kwargs: dict[str, object] = {}
    m = _ai_model.get()
    if m is not None:
        kwargs["model"] = m
        # When using anthropic/ prefix, litellm needs the Anthropic API key
        # and base URL — not the OpenRouter ones from ai_config.
        if m.startswith("anthropic/"):
            anthropic_key = os.getenv("ANTHROPIC_API_KEY", "")
            if anthropic_key:
                kwargs["api_key"] = anthropic_key
    return kwargs
