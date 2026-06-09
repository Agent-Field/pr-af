"""The PR-AF human-in-the-loop review gate.

Builds a hax form that shows the reviewer a short blurb of the PR's intent plus
every individual finding, then pauses the workflow until a workspace member:

  * **post_selected** — post the checked subset of findings, or
  * **rerun** — re-run the review with free-text instructions (e.g. "too
    aggressive, tone it down"), or
  * **reject** — post nothing.

First responder wins: the single hax callback that resolves ``app.pause``
decides the outcome.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

from ..schemas.severity import normalize_severity
from .client import (
    create_hax_form_request_with_timeout,
    extract_values_from_raw,
)

if TYPE_CHECKING:
    from hax import HaxClient

    from ..schemas.output import ScoredFinding


# The hax template this gate renders into. Registered in hax-sdk under
# src/templates/pr-af-review (id "pr-af-review-v1"). The template emits the same
# ``{template, values: {...}}`` response envelope form-builder uses, so the
# decision parser below is template-agnostic.
HAX_REVIEW_TEMPLATE = "pr-af-review-v1"

# Action chosen by the reviewer in the template's action buttons. These string
# values are the contract with the hax template's response `values.action`.
ACTION_POST = "post_selected"
ACTION_RERUN = "rerun"
ACTION_REJECT = "reject"
_VALID_ACTIONS = {ACTION_POST, ACTION_RERUN, ACTION_REJECT}

# Longest PR-intent blurb shown in the form. The raw PR body (e.g. a Dependabot
# changelog) is stripped of HTML and truncated to this so the form stays legible.
_MAX_INTENT_CHARS = 700

_HTML_TAG_RE = re.compile(r"<[^>]+>")
_WS_RE = re.compile(r"[ \t]+")
_BLANKLINES_RE = re.compile(r"\n{3,}")


@dataclass
class ReviewDecision:
    """Parsed outcome of one HITL round."""

    action: str  # ACTION_POST | ACTION_RERUN | ACTION_REJECT
    selected_finding_ids: set[str] = field(default_factory=set)
    instructions: str = ""
    # Underlying agentfield decision string ("approved", "expired", ...) for logs.
    decision_raw: str = ""

    @property
    def is_post(self) -> bool:
        return self.action == ACTION_POST

    @property
    def is_rerun(self) -> bool:
        return self.action == ACTION_RERUN

    @property
    def is_reject(self) -> bool:
        return self.action == ACTION_REJECT


def clean_intent(text: str | None, max_chars: int = _MAX_INTENT_CHARS) -> str:
    """Turn a raw PR body into a short, legible intent blurb.

    PR bodies (especially bot-authored ones like Dependabot) are often a wall of
    HTML — ``<details>``, ``<a href>``, changelog tables. The hax template
    renders the intent as markdown, where raw HTML shows up literally, so we
    strip tags, collapse whitespace, and truncate.
    """
    if not text:
        return ""
    stripped = _HTML_TAG_RE.sub(" ", text)
    stripped = _WS_RE.sub(" ", stripped)
    stripped = _BLANKLINES_RE.sub("\n\n", stripped)
    stripped = "\n".join(line.strip() for line in stripped.splitlines())
    stripped = stripped.strip()
    if len(stripped) > max_chars:
        stripped = stripped[:max_chars].rstrip() + "…"
    return stripped


def _finding_payload(finding: ScoredFinding) -> dict[str, Any]:
    """One finding entry for the hax template (camelCase per its zod schema)."""
    entry: dict[str, Any] = {
        "id": finding.id,
        # Last line of defense before the hax-sdk zod enum: a finding built off
        # the validated path could still carry a stray label. Normalize so the
        # create never 422s on severity.
        "severity": normalize_severity(finding.severity),
        "title": finding.title,
        # All findings start checked, matching the prior "submit posts all" default.
        "defaultSelected": True,
    }
    if finding.file_path:
        entry["filePath"] = finding.file_path
    if finding.line_start and finding.line_start > 0:
        entry["lineStart"] = finding.line_start
    if finding.line_end and finding.line_end > 0:
        entry["lineEnd"] = finding.line_end
    if finding.body:
        entry["body"] = finding.body
    if finding.suggestion:
        entry["suggestion"] = finding.suggestion
    if finding.dimension_name:
        entry["dimension"] = finding.dimension_name
    if finding.confidence is not None:
        entry["confidence"] = finding.confidence
    return entry


def build_review_payload(
    *,
    pr_intent: str,
    findings: list[ScoredFinding],
    title: str,
    pr_meta: dict[str, Any] | None = None,
    revision_iter: int = 0,
    revision_history: list[str] | None = None,
) -> dict[str, Any]:
    """Build the ``pr-af-review-v1`` request payload (validated server-side).

    Shape matches ``prAfReviewPayloadSchema`` in the hax-sdk template.
    """
    counts: dict[str, int] = {}
    for f in findings:
        counts[f.severity] = counts.get(f.severity, 0) + 1
    count_str = ", ".join(f"{n} {sev}" for sev, n in counts.items()) or "no findings"

    payload: dict[str, Any] = {
        "title": title,
        "intent": clean_intent(pr_intent),
        "reviewSummary": f"PR-AF found {len(findings)} finding(s) ({count_str}).",
        "findings": [_finding_payload(f) for f in findings],
        "postLabel": "Post selected",
        "rerunLabel": "Re-review with instructions",
        "rejectLabel": "Reject",
        "instructionsPlaceholder": "e.g. too aggressive, tone it down and drop the nitpicks",
    }
    if pr_meta:
        # Drop empties so optional zod fields stay absent rather than "".
        cleaned = {k: v for k, v in pr_meta.items() if v not in (None, "")}
        if cleaned:
            payload["pr"] = cleaned
    if revision_iter > 0 or revision_history:
        payload["revision"] = {
            "iteration": revision_iter,
            "priorInstructions": [i for i in (revision_history or []) if i and i.strip()],
        }
    return payload


def _coerce_str(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, list) and value:
        first = value[0]
        return first.strip() if isinstance(first, str) else str(first)
    return ""


def _coerce_id_list(value: Any) -> list[str]:
    if isinstance(value, list):
        return [v for v in value if isinstance(v, str)]
    if isinstance(value, str) and value:
        return [value]
    return []


def parse_review_decision(approval_result: Any, all_finding_ids: list[str]) -> ReviewDecision:
    """Convert an agentfield ``ApprovalResult`` into a ``ReviewDecision``.

    Terminal control-plane outcomes (expired/error, or a hax-level reject with
    no form values) map to a reject. Otherwise the reviewer's ``action`` radio
    drives the outcome; an absent ``findings_to_post`` field defaults to posting
    everything (the form pre-checks all findings).
    """
    decision = (getattr(approval_result, "decision", "") or "").strip()
    feedback = (getattr(approval_result, "feedback", "") or "").strip()
    values = extract_values_from_raw(getattr(approval_result, "raw_response", None))

    if decision in {"expired", "error"}:
        return ReviewDecision(action=ACTION_REJECT, instructions=feedback, decision_raw=decision)
    if decision == "rejected" and not values:
        return ReviewDecision(action=ACTION_REJECT, instructions=feedback, decision_raw=decision)

    action = _coerce_str(values.get("action"))
    if action not in _VALID_ACTIONS:
        action = ACTION_REJECT if decision == "rejected" else ACTION_POST
    instructions = _coerce_str(values.get("instructions")) or feedback

    if action == ACTION_RERUN:
        return ReviewDecision(action=ACTION_RERUN, instructions=instructions, decision_raw=decision)
    if action == ACTION_REJECT:
        return ReviewDecision(action=ACTION_REJECT, instructions=instructions, decision_raw=decision)

    # post_selected: honor the checked subset; default to all when field absent.
    if "findings_to_post" in values:
        selected = set(_coerce_id_list(values.get("findings_to_post")))
    else:
        selected = set(all_finding_ids)
    return ReviewDecision(
        action=ACTION_POST,
        selected_finding_ids=selected,
        instructions=instructions,
        decision_raw=decision,
    )


async def request_review_approval(
    *,
    app: Any,
    hax_client: HaxClient,
    pr_intent: str,
    findings: list[ScoredFinding],
    pr_label: str,
    webhook_url: str | None,
    user_id: str | None,
    expires_in_hours: int,
    pr_meta: dict[str, Any] | None = None,
    revision_iter: int = 0,
    revision_history: list[str] | None = None,
    metadata: dict[str, Any] | None = None,
) -> ReviewDecision:
    """Build the payload, create the hax request, pause, return the decision.

    Any failure to create the request or pause is surfaced as a reject so the
    pipeline never posts an unreviewed review when the gate is enabled.
    """
    title = "PR-AF Review Approval"
    if revision_iter > 0:
        title = f"{title} (revision {revision_iter})"
    if pr_label:
        title = f"{title} — {pr_label}"

    try:
        payload = build_review_payload(
            pr_intent=pr_intent,
            findings=findings,
            title=title,
            pr_meta=pr_meta,
            revision_iter=revision_iter,
            revision_history=revision_history,
        )
    except Exception as exc:
        app.note(
            f"hitl: failed to build review payload: {exc}",
            tags=["hitl", "payload", "error"],
        )
        return ReviewDecision(action=ACTION_REJECT, instructions=f"payload build failed: {exc}")

    try:
        created = await create_hax_form_request_with_timeout(
            app=app,
            hax_client=hax_client,
            payload=payload,
            request_type=HAX_REVIEW_TEMPLATE,
            title=title,
            description=None,
            expires_in_seconds=expires_in_hours * 3600,
            user_id=user_id,
            webhook_url=webhook_url,
            metadata=metadata,
        )
    except Exception as exc:
        app.note(
            f"hitl: create_request failed, treating as reject: {exc}",
            tags=["hitl", "hax", "error"],
        )
        return ReviewDecision(action=ACTION_REJECT, instructions=f"create_request failed: {exc}")

    try:
        approval_result = await app.pause(
            approval_request_id=created.id,
            approval_request_url=created.url,
            expires_in_hours=expires_in_hours,
        )
    except Exception as exc:
        app.note(
            f"hitl: pause failed, treating as reject: {exc}",
            tags=["hitl", "pause", "error"],
        )
        return ReviewDecision(action=ACTION_REJECT, instructions=f"pause failed: {exc}")

    decision = parse_review_decision(approval_result, [f.id for f in findings])
    app.note(
        f"hitl: review decision={decision.action} "
        f"(raw={decision.decision_raw}, selected={len(decision.selected_finding_ids)})",
        tags=["hitl", "decision", decision.action],
    )
    return decision
