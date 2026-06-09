"""Parallel polish pass — rewrites each inline comment body to be concise and
developer-focused right before posting to GitHub.

One `.ai()` call per comment, fired in parallel. Returns rewritten bodies.
On any per-comment failure, the original body is kept.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from .schemas.output import GitHubComment


_POLISH_SYSTEM = (
    "You rewrite GitHub PR review comments. A good PR comment tells the author "
    "exactly what to fix and why, so they can act in under 30 seconds. Open with a "
    "one-sentence directive. Then one short paragraph (2-3 sentences) on the concrete "
    "failure mode — no abstract security lectures, no 'attacker-controlled' filler. "
    "Preserve every file path, line number, identifier, code block, markdown "
    "header, GitHub alert callout (`> [!CAUTION]`, `> [!NOTE]`), `<details>` block, "
    "and `<sub>` line verbatim. Never invent facts. Never soften severity. Output "
    "the polished comment body only — no preamble, no commentary."
)


async def _polish_one(app: Any, body: str) -> str:
    try:
        out = await app.ai(
            f"Rewrite this PR review comment to be concise and developer-focused.\n\n{body}",
            system=_POLISH_SYSTEM,
        )
    except Exception as exc:  # noqa: BLE001
        print(f"[PR-AF] Polish skipped: {exc.__class__.__name__}", flush=True)
        return body
    text = getattr(out, "text", None) or getattr(out, "content", None) or str(out)
    text = text.strip()
    return text or body


async def polish_comments(app: Any, comments: list[GitHubComment]) -> list[GitHubComment]:
    """Rewrite each comment body in parallel. Returns a new list."""
    if not comments:
        return comments
    new_bodies = await asyncio.gather(*(_polish_one(app, c.body) for c in comments))
    polished = [c.model_copy(update={"body": b}) for c, b in zip(comments, new_bodies, strict=True)]
    changed = sum(1 for c, b in zip(comments, new_bodies, strict=True) if b != c.body)
    print(f"[PR-AF] Polish complete: {changed}/{len(comments)} comments rewritten", flush=True)
    return polished
