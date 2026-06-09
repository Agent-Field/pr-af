"""Human-in-the-loop review gate for PR-AF.

Mirrors SWE-AF's plan-phase approval: when ``HAX_API_KEY`` is set, PR-AF pauses
before posting a review and routes the findings to a hax workspace member for
per-finding approval, re-review, or rejection.
"""

from __future__ import annotations

from .client import (
    approval_webhook_url,
    build_hax_client_from_env,
    create_hax_form_request_with_timeout,
    extract_values_from_raw,
)
from .review_gate import (
    ACTION_POST,
    ACTION_REJECT,
    ACTION_RERUN,
    HAX_REVIEW_TEMPLATE,
    ReviewDecision,
    build_review_payload,
    clean_intent,
    parse_review_decision,
    request_review_approval,
)

__all__ = [
    "ACTION_POST",
    "ACTION_REJECT",
    "ACTION_RERUN",
    "HAX_REVIEW_TEMPLATE",
    "ReviewDecision",
    "approval_webhook_url",
    "build_hax_client_from_env",
    "build_review_payload",
    "clean_intent",
    "create_hax_form_request_with_timeout",
    "extract_values_from_raw",
    "parse_review_decision",
    "request_review_approval",
]
