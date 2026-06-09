"""hax-sdk plumbing for the PR-AF human-in-the-loop review gate.

Ported from SWE-AF's ``swe_af.hitl.ask_user`` — the hax client builder, the
control-plane webhook URL resolver, the watchdog-safe ``create_request``
wrapper, and the helper that digs form values out of an ``ApprovalResult``.

``hax`` is imported lazily so this module (and the orchestrator that imports it)
stays importable in environments without the SDK installed.
"""

from __future__ import annotations

import asyncio
import os
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from hax import HaxClient


# Same default as SWE-AF: hax service on :3000, REST under /api/v1.
_DEFAULT_HAX_BASE = "http://localhost:3000"

# SWE-AF's lesson (test_pause_watchdog_e2e.py): the synchronous
# hax_client.create_request can wedge for tens of minutes and burn the
# reasoner's active-time budget, so every call is wrapped in a hard timeout.
HAX_CREATE_REQUEST_TIMEOUT_SECONDS = 120.0


def build_hax_client_from_env() -> HaxClient | None:
    """Construct a ``HaxClient`` from ``HAX_API_KEY`` / ``HAX_SDK_URL``.

    Returns ``None`` when ``HAX_API_KEY`` is unset or empty — callers treat that
    as "HITL disabled" and post the review directly. This is the on/off switch.
    """
    api_key = os.environ.get("HAX_API_KEY", "").strip()
    if not api_key:
        return None
    from hax import HaxClient

    base = os.environ.get("HAX_SDK_URL", _DEFAULT_HAX_BASE).rstrip("/")
    return HaxClient(api_key=api_key, base_url=f"{base}/api/v1")


def approval_webhook_url(app: Any) -> str | None:
    """Resolve the control-plane webhook URL for ``app.pause`` callbacks.

    Mirrors the URL SWE-AF's plan-approval gate uses
    (``{cp_base_url}/api/v1/webhooks/approval-response``). Returns ``None`` when
    no control-plane URL can be resolved.

    ``AGENTFIELD_PUBLIC_URL`` takes precedence: this callback is invoked by
    hax-sdk, which lives in a *separate* Railway project, so it must be a
    publicly reachable URL — not the ``AGENTFIELD_SERVER`` internal address
    (e.g. ``control-plane.railway.internal``) the agent uses to call the CP.
    Conflating the two is what made an answered review undeliverable: the
    webhook ``fetch failed`` because the internal host doesn't resolve
    cross-project. Falls back to ``AGENTFIELD_SERVER`` for single-project /
    local setups where the two are the same.
    """
    cp_base = (
        os.environ.get("AGENTFIELD_PUBLIC_URL")
        or getattr(app, "agentfield_server", None)
        or os.environ.get("AGENTFIELD_SERVER")
        or ""
    ).rstrip("/")
    if not cp_base:
        return None
    return f"{cp_base}/api/v1/webhooks/approval-response"


async def create_hax_form_request_with_timeout(
    *,
    app: Any,
    hax_client: HaxClient,
    payload: dict[str, Any],
    request_type: str,
    title: str,
    description: str | None,
    expires_in_seconds: int,
    user_id: str | None,
    webhook_url: str | None,
    metadata: dict[str, Any] | None,
    timeout_seconds: float = HAX_CREATE_REQUEST_TIMEOUT_SECONDS,
) -> Any:
    """Submit a hax request of ``request_type`` with a hard timeout.

    Runs the synchronous ``hax_client.create_request`` in a worker thread under
    ``asyncio.wait_for`` so a wedged hax-sdk fails fast (``RuntimeError``)
    instead of silently burning the reasoner's active-time budget. Returns the
    ``CreatedRequest``; the caller passes ``.id`` / ``.url`` to ``app.pause``.

    ``payload`` must satisfy the registered hax template's schema — the hax
    service validates it server-side and rejects unknown ``request_type`` values.
    """
    app.note(
        f"hitl: submitting hax request ({request_type}: {title!r})",
        tags=["hitl", "hax", "create_request"],
    )

    kwargs: dict[str, Any] = {
        "type": request_type,
        "payload": payload,
        "title": title,
        "expires_in_seconds": expires_in_seconds,
    }
    if description is not None:
        kwargs["description"] = description
    if user_id is not None:
        kwargs["user_id"] = user_id
    if webhook_url is not None:
        kwargs["webhook_url"] = webhook_url
    if metadata is not None:
        kwargs["metadata"] = metadata

    try:
        created = await asyncio.wait_for(
            asyncio.to_thread(hax_client.create_request, **kwargs),
            timeout=timeout_seconds,
        )
    except TimeoutError as exc:
        app.note(
            f"hitl: hax create_request timed out after {timeout_seconds}s",
            tags=["hitl", "hax", "timeout"],
        )
        raise RuntimeError(
            f"hax-sdk create_request (form-builder) timed out after "
            f"{timeout_seconds}s; hax-sdk is likely wedged."
        ) from exc
    except Exception as exc:
        app.note(
            f"hitl: hax create_request raised {type(exc).__name__}: {exc}",
            tags=["hitl", "hax", "error"],
        )
        raise

    app.note(
        f"hitl: hax form request created (request_id={created.id})",
        tags=["hitl", "hax", "submitted"],
    )
    return created


def extract_values_from_raw(raw: Any) -> dict[str, Any]:
    """Find the submitted form values inside an ``ApprovalResult.raw_response``.

    hax delivers values at ``raw['values']`` or ``raw['response']['values']``
    depending on the callback shape; check both.
    """
    if not isinstance(raw, dict):
        return {}
    direct = raw.get("values")
    if isinstance(direct, dict):
        return dict(direct)
    response_obj = raw.get("response")
    if isinstance(response_obj, dict):
        inner = response_obj.get("values")
        if isinstance(inner, dict):
            return dict(inner)
    return {}
