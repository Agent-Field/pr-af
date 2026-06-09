"""Canonical finding-severity vocabulary — the single source of truth.

PR-AF findings must use exactly one of four severities. This is a *cross-repo
contract*: the hax-sdk ``pr-af-review-v1`` template enforces the same set as a
zod enum, and rejects the whole HITL request if a finding carries anything else
(this is what silently swallowed a real review — a model-emitted ``"high"``
failed the template's enum, the create 422'd, and the gate treated it as a
reject → nothing posted).

To keep the model honest *and* the pipeline robust we do two things with this
module:

1. Type finding-severity fields as :data:`Severity` (an ``Annotated`` ``Literal``)
   so the JSON schema handed to the model advertises the enum, **and** so a
   :func:`normalize_severity` ``BeforeValidator`` coerces stray values
   (``high`` → ``important``) instead of failing validation — deterministic and
   cheap, with no retry storms or dropped-findings fallout.
2. Re-apply :func:`normalize_severity` at the hax-sdk boundary as defense in
   depth, so a finding constructed off the validated path can never break the
   create.
"""

from __future__ import annotations

from typing import Annotated, Literal

from pydantic import BeforeValidator

#: The four canonical severities, in *decreasing* order of urgency.
SeverityLiteral = Literal["critical", "important", "suggestion", "nitpick"]

#: Tuple form for membership checks / iteration.
VALID_SEVERITIES: tuple[str, ...] = (
    "critical",
    "important",
    "suggestion",
    "nitpick",
)

#: Where unknown / empty labels land. "suggestion" is the mid-low rung — it
#: keeps the finding visible without inflating it to a blocker.
DEFAULT_SEVERITY: SeverityLiteral = "suggestion"

# Common labels models reach for, mapped onto the 4-level scale by rank.
# critical > important > suggestion > nitpick.
_SEVERITY_ALIASES: dict[str, SeverityLiteral] = {
    # canonical (identity)
    "critical": "critical",
    "important": "important",
    "suggestion": "suggestion",
    "nitpick": "nitpick",
    # critical-tier synonyms
    "blocker": "critical",
    "fatal": "critical",
    "severe": "critical",
    # important-tier synonyms (the "high" that caused the incident)
    "high": "important",
    "error": "important",
    "major": "important",
    # suggestion-tier synonyms
    "medium": "suggestion",
    "moderate": "suggestion",
    "warning": "suggestion",
    "warn": "suggestion",
    # nitpick-tier synonyms
    "low": "nitpick",
    "minor": "nitpick",
    "nit": "nitpick",
    "info": "nitpick",
    "informational": "nitpick",
    "trivial": "nitpick",
}


def normalize_severity(
    value: object, default: SeverityLiteral = DEFAULT_SEVERITY
) -> SeverityLiteral:
    """Coerce an arbitrary severity label to the canonical vocabulary.

    Case-insensitive and whitespace-tolerant. Known synonyms map by rank
    (``high`` → ``important``, ``medium`` → ``suggestion``, ``low`` →
    ``nitpick``); empty, non-string, or unrecognized input falls back to
    ``default``.
    """
    if not isinstance(value, str):
        return default
    key = value.strip().lower()
    if not key:
        return default
    return _SEVERITY_ALIASES.get(key, default)


#: Field type for finding severities: advertises the enum in the JSON schema
#: (so the model is guided) while coercing stray labels before validation.
Severity = Annotated[SeverityLiteral, BeforeValidator(normalize_severity)]
