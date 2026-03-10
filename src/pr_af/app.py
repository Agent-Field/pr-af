from __future__ import annotations

# pyright: reportMissingImports=false

import os
import subprocess
from pathlib import Path
from typing import Any, cast

import agentfield as _agentfield
from dotenv import load_dotenv

_project_root = Path(__file__).resolve().parents[2]
load_dotenv(_project_root / ".env")

from fastapi import HTTPException

from agentfield import AIConfig, Agent

from .config import AIIntegrationConfig, ReviewConfig
from .orchestrator import ReviewOrchestrator
from .reasoners import router as reasoner_router
from .schemas.input import ReviewInput  # noqa: TC001

_ai_config = AIIntegrationConfig.from_env()
NODE_ID = os.getenv("PR_AF", "pr-af")
HarnessConfig = getattr(_agentfield, "HarnessConfig")

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


def _resolve_repo(repo_path: str | None, pr_url: str | None) -> str:
    target = repo_path
    if not target and isinstance(pr_url, str) and "github.com" in pr_url and "/pull/" in pr_url:
        parts = pr_url.split("github.com/")[-1].split("/pull/")[0].strip("/")
        if parts.count("/") == 1:
            target = f"https://github.com/{parts}.git"

    if isinstance(target, str) and os.path.isdir(target):
        return str(Path(target).resolve())

    if isinstance(target, str) and target.startswith(("https://", "http://", "git@")):
        repo_name = target.rstrip("/").split("/")[-1].replace(".git", "")
        target_dir = f"/workspaces/{repo_name}"
        os.makedirs("/workspaces", exist_ok=True)

        if os.path.isdir(target_dir) and os.path.isdir(os.path.join(target_dir, ".git")):
            subprocess.run(
                ["git", "pull", "--ff-only"],
                cwd=target_dir,
                env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "echo"},
                timeout=60,
                capture_output=True,
            )
            return target_dir

        clone_url = target
        gh_token = os.getenv("GH_TOKEN") or os.getenv("GITHUB_TOKEN", "")
        if gh_token and clone_url.startswith("https://github.com/"):
            clone_url = clone_url.replace("https://github.com/", f"https://{gh_token}@github.com/")

        result = subprocess.run(
            ["git", "clone", "--depth", "1", clone_url, target_dir],
            env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "echo"},
            timeout=120,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise ValueError(f"git clone failed: {result.stderr.strip()}")
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
    max_cost_usd: float = 2.0,
    max_duration_seconds: int = 300,
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
) -> dict[str, object]:
    print(
        f"[PR-AF DEBUG] review() called with pr_url={pr_url!r}, diff_text={'<set>' if diff_text else None}, repo_path={repo_path!r}, depth={depth!r}, dry_run={dry_run!r}",
        flush=True,
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


async def health() -> dict[str, str]:
    return {"status": "healthy", "version": "0.1.0"}


cast("Any", app).add_api_route("/health", health, methods=["GET"])


app.include_router(reasoner_router)


def main() -> None:
    app.run(port=8004, host="0.0.0.0")


if __name__ == "__main__":
    main()
