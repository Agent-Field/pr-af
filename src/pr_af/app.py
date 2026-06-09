from __future__ import annotations

# pyright: reportMissingImports=false
import hashlib
import hmac
import json
import os
import subprocess
from pathlib import Path
from typing import Any, cast

import agentfield as _agentfield
import httpx
from agentfield import Agent, AIConfig
from dotenv import load_dotenv
from fastapi import HTTPException, Request

from .config import AIIntegrationConfig, ReviewConfig
from .orchestrator import ReviewOrchestrator
from .reasoners import router as reasoner_router
from .schemas.input import ReviewInput  # noqa: TC001

_project_root = Path(__file__).resolve().parents[2]
load_dotenv(_project_root / ".env")

_ai_config = AIIntegrationConfig.from_env()
NODE_ID = os.getenv("NODE_ID", "pr-af")
HarnessConfig = _agentfield.HarnessConfig

app = Agent(
    node_id=NODE_ID,
    version="0.1.0",
    description="AI-Native Pull Request Review Agent",
    agentfield_server=os.getenv("AGENTFIELD_SERVER", "http://localhost:8080"),
    callback_url=os.getenv("AGENT_CALLBACK_URL", "http://127.0.0.1:8004"),
    api_key=os.getenv("AGENTFIELD_API_KEY"),
    harness_config=HarnessConfig(
        provider=_ai_config.provider,
        model=_ai_config.harness_model,
        max_turns=_ai_config.max_turns,
        env=_ai_config.provider_env(),
        opencode_bin=_ai_config.opencode_bin,
        permission_mode="auto",
    ),
    ai_config=AIConfig(
        model=_ai_config.ai_model,
        api_key=os.getenv("OPENROUTER_API_KEY", ""),
        api_base="https://openrouter.ai/api/v1",
    ),
)


def _extract_pr_number(pr_url: str) -> int | None:
    if "github.com" in pr_url and "/pull/" in pr_url:
        try:
            return int(pr_url.split("/pull/")[-1].split("/")[0].strip("/"))
        except (ValueError, IndexError):
            return None
    return None


def _resolve_budget_caps(
    max_cost_usd: float | None, max_duration_seconds: int | None
) -> tuple[float, int]:
    """Resolve the review budget caps.

    When the caller does not pass an explicit value, fall back to the
    ``PR_AF_MAX_COST_USD`` / ``PR_AF_MAX_DURATION_SECONDS`` env vars (so the
    budget can be tuned per deployment without a code change), and finally to
    the historical defaults (2.0 USD / 300s) when neither is set. An explicit
    argument always wins over the env var.
    """
    if max_cost_usd is None:
        max_cost_usd = float(os.getenv("PR_AF_MAX_COST_USD", "2.0"))
    if max_duration_seconds is None:
        max_duration_seconds = int(os.getenv("PR_AF_MAX_DURATION_SECONDS", "300"))
    return max_cost_usd, max_duration_seconds


def _checkout_pr_branch(target_dir: str, pr_number: int) -> None:
    git_env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "echo"}
    # Fetch the PR head into FETCH_HEAD rather than directly into a local
    # ``pr-review`` branch. When the workspace is reused across reviews, the
    # previous run leaves ``pr-review`` checked out, and
    # ``git fetch origin pull/N/head:pr-review`` is *refused* by git
    # ("refusing to fetch into branch '...' checked out at ..."). That failure
    # was previously swallowed (capture_output, no return-code check), so every
    # PR after the first in a workspace's lifetime got reviewed against the
    # first PR's tree — producing silent "no findings" reviews. Fetching to
    # FETCH_HEAD always succeeds, and ``checkout -B`` then (re)points
    # ``pr-review`` at it even when it is the currently checked-out branch.
    fetch = subprocess.run(
        ["git", "-C", target_dir, "fetch", "--depth", "1", "origin", f"pull/{pr_number}/head"],
        env=git_env,
        timeout=300,
        capture_output=True,
        text=True,
    )
    if fetch.returncode != 0:
        raise ValueError(f"git fetch of PR #{pr_number} head failed: {fetch.stderr.strip()}")
    checkout = subprocess.run(
        ["git", "-C", target_dir, "checkout", "-B", "pr-review", "FETCH_HEAD"],
        env=git_env,
        timeout=30,
        capture_output=True,
        text=True,
    )
    if checkout.returncode != 0:
        raise ValueError(f"git checkout of PR #{pr_number} (pr-review) failed: {checkout.stderr.strip()}")


def _resolve_repo(repo_path: str | None, pr_url: str | None) -> str:
    workdir = os.getenv("PR_AF_WORKDIR", "/workspaces")
    target = repo_path
    pr_number: int | None = None

    if not target and isinstance(pr_url, str) and "github.com" in pr_url and "/pull/" in pr_url:
        parts = pr_url.split("github.com/")[-1].split("/pull/")[0].strip("/")
        if parts.count("/") == 1:
            target = f"https://github.com/{parts}.git"
        pr_number = _extract_pr_number(pr_url)

    if isinstance(target, str) and os.path.isdir(target):
        return str(Path(target).resolve())

    if isinstance(target, str) and target.startswith(("https://", "http://", "git@")):
        repo_name = target.rstrip("/").split("/")[-1].replace(".git", "")
        target_dir = os.path.join(workdir, repo_name)
        os.makedirs(workdir, exist_ok=True)

        clone_url = target
        gh_token = os.getenv("GH_TOKEN", "")
        if gh_token and clone_url.startswith("https://github.com/"):
            clone_url = clone_url.replace("https://github.com/", f"https://{gh_token}@github.com/")

        git_env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "echo"}
        clone_timeout = 600  # Large repos (e.g. TrueNAS middleware) need time

        if os.path.isdir(target_dir) and os.path.isdir(os.path.join(target_dir, ".git")):
            subprocess.run(
                ["git", "-C", target_dir, "fetch", "--all"],
                env=git_env,
                timeout=clone_timeout,
                capture_output=True,
            )
        else:
            # Shallow clone: only need enough history to read files, not full history
            clone_cmd = ["git", "clone", "--depth", "1", "--no-tags", clone_url, target_dir]
            # If we know the PR number, skip default branch checkout — we'll fetch the PR ref
            if pr_number:
                clone_cmd = [
                    "git",
                    "clone",
                    "--depth",
                    "1",
                    "--no-tags",
                    "--no-checkout",
                    clone_url,
                    target_dir,
                ]
            result = subprocess.run(
                clone_cmd,
                env=git_env,
                timeout=clone_timeout,
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                raise ValueError(f"git clone failed: {result.stderr.strip()}")

        if pr_number:
            _checkout_pr_branch(target_dir, pr_number)

        return target_dir

    return str(Path(os.getenv("PR_AF_REPO_PATH", os.getcwd())).resolve())


@app.reasoner()
async def review(
    pr_url: str | None = None,
    diff_text: str | None = None,
    repo_path: str | None = None,
    base_ref: str | None = None,
    head_ref: str | None = None,
    depth: str = "auto",
    max_cost_usd: float | None = None,
    max_duration_seconds: int | None = None,
    focus: str = "auto",
    ignore_paths: list[str] | None = None,
    hints: list[str] | None = None,
    models: dict[str, str] | None = None,
    max_concurrent_reviewers: int | None = None,
    max_coverage_iterations: int | None = None,
    max_review_depth: int = 2,
    output_format: str = "github",
    dry_run: bool = False,
    post_pr_number: int | None = None,
    suggestion_mode: str = "comment",
) -> dict[str, object]:
    print(
        f"[PR-AF DEBUG] review() called with pr_url={pr_url!r}, "
        f"diff_text={'<set>' if diff_text else None}, repo_path={repo_path!r}, "
        f"depth={depth!r}, dry_run={dry_run!r}",
        flush=True,
    )
    max_cost_usd, max_duration_seconds = _resolve_budget_caps(
        max_cost_usd, max_duration_seconds
    )
    review_input = ReviewInput(
        pr_url=pr_url,
        diff_text=diff_text,
        repo_path=repo_path,
        base_ref=base_ref,
        head_ref=head_ref,
        depth=depth,
        max_cost_usd=max_cost_usd,
        max_duration_seconds=max_duration_seconds,
        focus=focus,
        ignore_paths=ignore_paths or [],
        hints=hints or [],
        models=models,
        max_concurrent_reviewers=max_concurrent_reviewers,
        max_coverage_iterations=max_coverage_iterations,
        max_review_depth=min(max_review_depth, 3),
        output_format=output_format,
        dry_run=dry_run,
        post_pr_number=post_pr_number,
        suggestion_mode=suggestion_mode,
    )
    resolved_repo_path = _resolve_repo(review_input.repo_path, review_input.pr_url)
    if not review_input.repo_path:
        review_input = review_input.model_copy(update={"repo_path": resolved_repo_path})
    config = ReviewConfig.from_input(review_input)
    orchestrator = ReviewOrchestrator(app=app, input=review_input, config=config)
    try:
        result = await orchestrator.run()
    except ValueError as exc:
        raise HTTPException(status_code=400, detail={"error": str(exc)}) from exc
    except Exception as exc:
        import traceback as _tb

        print(f"[PR-AF] Pipeline error: {exc}\n{_tb.format_exc()}", flush=True)
        cast("Any", app).note(f"Review pipeline failed: {exc}", tags=["review", "error"])
        raise HTTPException(status_code=500, detail={"error": f"review execution failed: {exc}"}) from exc

    return result.model_dump()


# ---------------------------------------------------------------------------
# GitHub Webhook — @mention-triggered PR review
# ---------------------------------------------------------------------------
_BOT_MENTION = os.getenv("PR_AF_BOT_MENTION", "@pr-af")
_WEBHOOK_SECRET = os.getenv("GITHUB_WEBHOOK_SECRET", "")
_CP_URL = os.getenv("AGENTFIELD_SERVER", "http://localhost:8080")


def _verify_signature(payload: bytes, signature: str, secret: str) -> bool:
    if not secret:
        return True  # no secret configured — skip verification
    expected = "sha256=" + hmac.new(
        secret.encode(), payload, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature)


async def _fire_review(
    pr_url: str, hints: list[str] | None = None
) -> str | None:
    """Fire an async review execution via the Control Plane. Returns exec id."""
    input_payload: dict[str, object] = {
        "pr_url": pr_url,
        "depth": "standard",
        "dry_run": False,
    }
    if hints:
        input_payload["hints"] = hints
    body = json.dumps({"input": input_payload})
    try:
        async with httpx.AsyncClient(timeout=15.0) as client:
            resp = await client.post(
                f"{_CP_URL}/api/v1/execute/async/pr-af.review",
                content=body,
                headers={"Content-Type": "application/json"},
            )
            resp.raise_for_status()
            return resp.json().get("execution_id")
    except Exception as exc:
        print(f"[PR-AF] Failed to fire review: {exc}", flush=True)
        return None


def _extract_hints_from_comment(comment_body: str) -> list[str]:
    """Extract review hints from the text after the @mention."""
    mention = _BOT_MENTION.lower()
    lower = comment_body.lower()
    idx = lower.find(mention)
    if idx < 0:
        return []
    after = comment_body[idx + len(mention) :].strip()
    if after:
        return [after]
    return []


def _get_pr_url_from_issue(payload: dict) -> str | None:
    """Extract PR URL from an issue_comment webhook payload."""
    issue = payload.get("issue", {})
    pr_data = issue.get("pull_request", {})
    return pr_data.get("html_url") or None


async def webhook_github(request: Request) -> dict[str, object]:
    """Handle GitHub webhook for @mention-triggered PR reviews.

    Listens for issue_comment events. When someone comments on a PR with
    @pr-af (or the configured bot mention), fires an async review via the
    Control Plane. Any text after the @mention is passed as review hints.

    Examples:
        "@pr-af" — standard review
        "@pr-af please focus on error handling and security" — guided review
    """
    body = await request.body()

    sig = request.headers.get("x-hub-signature-256", "")
    if _WEBHOOK_SECRET and not _verify_signature(body, sig, _WEBHOOK_SECRET):
        raise HTTPException(status_code=401, detail="Invalid signature")

    event = request.headers.get("x-github-event", "")
    if event == "ping":
        return {"status": "pong"}

    if event != "issue_comment":
        return {"status": "ignored", "reason": f"event={event}"}

    payload = json.loads(body)
    action = payload.get("action", "")
    if action != "created":
        return {"status": "ignored", "reason": f"action={action}"}

    comment_body = payload.get("comment", {}).get("body", "")
    if _BOT_MENTION.lower() not in comment_body.lower():
        return {"status": "ignored", "reason": "no bot mention"}

    # Only respond to comments on PRs (issue_comment fires for issues too)
    pr_url = _get_pr_url_from_issue(payload)
    if not pr_url:
        return {"status": "ignored", "reason": "not a PR comment"}

    repo_name = payload.get("repository", {}).get("full_name", "")
    issue_number = payload.get("issue", {}).get("number")
    hints = _extract_hints_from_comment(comment_body)

    print(
        f"[PR-AF] Webhook: {_BOT_MENTION} mentioned in "
        f"{repo_name}#{issue_number} — firing review"
        + (f" with hints: {hints}" if hints else ""),
        flush=True,
    )
    exec_id = await _fire_review(pr_url, hints=hints or None)
    return {"status": "review_dispatched", "pr_url": pr_url, "execution_id": exec_id}


cast("Any", app).add_api_route(
    "/webhook/github", webhook_github, methods=["POST"]
)


async def health() -> dict[str, str]:
    return {"status": "healthy", "version": "0.1.0"}


cast("Any", app).add_api_route("/health", health, methods=["GET"])


app.include_router(reasoner_router)


def main() -> None:
    app.run(port=8004, host="0.0.0.0")


if __name__ == "__main__":
    main()
