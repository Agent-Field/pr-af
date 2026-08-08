from __future__ import annotations

import asyncio

import pytest

import pr_af.orchestrator as orchestrator_module
from pr_af.config import ReviewConfig
from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import ReviewInput
from pr_af.schemas.pipeline import ReviewDimension, ReviewPlan


@pytest.mark.asyncio
async def test_single_reviewer_permit_allows_spawned_sub_review(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config = ReviewConfig()
    config.budget.max_concurrent_reviewers = 1
    config.budget.max_review_depth = 1
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="diff"), config=config)
    calls: list[int] = []

    async def fake_review_dimension(**kwargs: object) -> dict[str, object]:
        depth = int(kwargs["current_depth"])
        calls.append(depth)
        if depth == 0:
            return {
                "findings": [],
                "sub_reviews": [
                    {
                        "review_prompt": "inspect the child",
                        "target_files": ["src/child.py"],
                        "reason": "child path",
                    }
                ],
            }
        return {
            "findings": [
                {
                    "title": "child completed",
                    "file_path": "src/child.py",
                    "line_start": 1,
                }
            ],
            "sub_reviews": [],
        }

    monkeypatch.setattr(orchestrator_module, "review_dimension", fake_review_dimension)
    plan = ReviewPlan(
        dimensions=[
            ReviewDimension(
                id="parent",
                name="Parent",
                review_prompt="inspect the parent",
                target_files=["src/parent.py"],
            )
        ]
    )
    queue: asyncio.Queue = asyncio.Queue()

    await asyncio.wait_for(orchestrator._run_parallel_review(plan, queue), timeout=1)

    batches = []
    while (batch := await queue.get()) is not None:
        batches.extend(batch)
    assert calls == [0, 1]
    assert [finding.title for finding in batches] == ["child completed"]
