"""PR-AF Pipeline Orchestrator.

Coordinates the 7-phase review pipeline. Manages budget, streaming queues,
and phase transitions. Follows the SEC-AF orchestrator pattern with
Contract-AF's streaming and meta-prompting additions.

This is the skeleton showing data flow between phases.
"""

from __future__ import annotations

import asyncio
import os
import subprocess
import time
from typing import Any, cast
from uuid import uuid4

import httpx

from .config import AUTO_DEPTH_THRESHOLDS, DEPTH_PROFILES, ReviewConfig
from .diff_engine import parse_unified_diff
from .github.client import GitHubClient
from .reasoners.harnesses import (
    adversary_phase,
    anatomy_phase,
    coverage_gate,
    cross_ref_phase,
    intake_phase,
    planning_phase,
    review_dimension,
)
from .schemas.input import ChangedFile, GitHubPRData, ReviewInput
from .schemas.output import (
    GitHubComment,
    GitHubReview,
    ReviewMetadata,
    ReviewResult,
    ReviewSummary,
    ScoredFinding,
)
from .schemas.pipeline import (
    AdversaryResult,
    AnatomyResult,
    CrossRefInteraction,
    IntakeResult,
    ReviewDimension,
    ReviewFinding,
    ReviewPlan,
    SubReviewRequest,
)
from .scoring import deduplicate_exact, determine_review_event, score_findings


class BudgetExhausted(RuntimeError):
    pass


def _unwrap(result: object) -> dict:
    if isinstance(result, dict):
        if "output" in result:
            return cast(dict, result["output"])
        if "result" in result:
            return cast(dict, result["result"])
    return cast(dict, result)


class ReviewOrchestrator:
    """Orchestrates the 7-phase PR review pipeline.

    Pipeline flow:
        Phase 1: INTAKE    (.ai() + .harness() fallback)
        Phase 2: ANATOMY   (code + .harness())
        Phase 3: PLANNING  (.harness() — meta-prompting)
        Phase 4: REVIEW    (N × .harness(), streaming)
        Phase 5: LAYER     (cross-ref + adversary + coverage, streaming)
        Phase 6: SYNTHESIS  (code — deterministic)
        Phase 7: OUTPUT    (code — GitHub API)
    """

    PHASE_ORDER = (
        "intake",
        "anatomy",
        "planning",
        "review",
        "cross_ref",
        "adversary",
        "coverage",
        "synthesis",
        "output",
    )

    def __init__(self, app: Any, input: ReviewInput, config: ReviewConfig | None = None):
        self.app = app
        self.input = input
        self.config = config or ReviewConfig()
        self.started_at = time.monotonic()
        self.review_id = f"rev_{uuid4().hex[:12]}"

        self.total_cost_usd = 0.0
        self.cost_breakdown: dict[str, float] = {phase: 0.0 for phase in self.PHASE_ORDER}
        self.agent_invocations = 0
        self.budget_exhausted = False

        self.pr_data: GitHubPRData | None = None
        self.intake_result: IntakeResult | None = None
        self.anatomy_result: AnatomyResult | None = None
        self.coverage_iterations = 0
        self.cross_ref_count = 0
        self.adversary_confirmed_count = 0
        self.adversary_challenged_count = 0

    async def run(self) -> ReviewResult:
        print("[PR-AF] Starting 7-phase pipeline", flush=True)

        print("[PR-AF] Phase 1: INTAKE", flush=True)
        intake = await self._run_intake()
        self.intake_result = intake
        review_depth = self._resolve_depth(intake)
        print(
            f"[PR-AF] Intake complete: type={intake.pr_type}, complexity={intake.complexity}, depth={review_depth}",
            flush=True,
        )

        print("[PR-AF] Phase 2: ANATOMY", flush=True)
        anatomy = await self._run_anatomy(intake)
        self.anatomy_result = anatomy
        print(f"[PR-AF] Anatomy complete: {len(anatomy.files)} files, {len(anatomy.clusters)} clusters", flush=True)

        print("[PR-AF] Phase 3: PLANNING", flush=True)
        plan = await self._run_planning(intake, anatomy, review_depth)

        print(f"[PR-AF] Planning complete: {len(plan.dimensions)} dimensions", flush=True)

        print("[PR-AF] Phase 4+5: REVIEW (parallel) + LAYER", flush=True)
        findings_queue: asyncio.Queue[list[ReviewFinding] | None] = asyncio.Queue()

        review_task = asyncio.create_task(self._run_parallel_review(plan, findings_queue))
        layer_task = asyncio.create_task(self._run_review_layer(plan, findings_queue, anatomy))

        _, layer_result = await asyncio.gather(review_task, layer_task)
        all_findings, cross_refs, adversary_results = layer_result

        print(
            f"[PR-AF] Review+Layer done: {len(all_findings)} findings, {len(cross_refs)} cross-refs, {len(adversary_results)} adversary results",
            flush=True,
        )

        print("[PR-AF] Phase 6: COVERAGE LOOP", flush=True)
        all_findings, cross_refs, adversary_results = await self._run_coverage_loop(
            plan, anatomy, all_findings, cross_refs, adversary_results
        )
        self.cross_ref_count = len(cross_refs)
        self.adversary_challenged_count = sum(1 for result in adversary_results if result.verdict == "challenged")
        self.adversary_confirmed_count = sum(1 for result in adversary_results if result.verdict == "confirmed")

        print("[PR-AF] Phase 7: SYNTHESIS", flush=True)
        scored_findings = self._synthesize(all_findings, cross_refs, adversary_results)
        print(f"[PR-AF] Synthesis complete: {len(scored_findings)} scored findings", flush=True)

        print("[PR-AF] Phase 8: OUTPUT", flush=True)
        result = await self._generate_output(scored_findings, intake, anatomy, plan)
        print(
            f"[PR-AF] Pipeline complete! {result.summary.total_findings} findings, cost=${result.summary.cost_usd}",
            flush=True,
        )

        return result

    async def _run_intake(self) -> IntakeResult:
        if self._budget_or_timeout_exhausted("intake"):
            raise BudgetExhausted("Budget exhausted before intake")

        if self.input.pr_url:
            client = GitHubClient()
            self.pr_data = await client.fetch_pr(self.input.pr_url)
        elif self.input.diff_text:
            parsed = parse_unified_diff(self.input.diff_text)
            self.pr_data = GitHubPRData(
                owner="",
                repo="",
                number=0,
                title="Local Diff Review",
                description="",
                diff=self.input.diff_text,
                changed_files=[self._to_changed_file(f) for f in parsed],
            )
        elif self.input.repo_path:
            diff = self._compute_repo_diff(
                repo_path=self.input.repo_path,
                base_ref=self.input.base_ref,
                head_ref=self.input.head_ref,
            )
            parsed = parse_unified_diff(diff)
            self.pr_data = GitHubPRData(
                owner="",
                repo="",
                number=self.input.post_pr_number or 0,
                title="Local Repository Review",
                description="",
                diff=diff,
                changed_files=[self._to_changed_file(f) for f in parsed],
            )
        else:
            raise ValueError("One of pr_url, diff_text, or repo_path is required")

        result_raw = await intake_phase(
            pr_data=self.pr_data.model_dump(),
            depth=self.input.depth,
        )
        self.agent_invocations += 1
        self._register_cost("intake", self._extract_cost(result_raw))
        intake = IntakeResult.model_validate(result_raw)
        return intake

    async def _run_anatomy(self, intake: IntakeResult) -> AnatomyResult:
        if self._budget_or_timeout_exhausted("anatomy"):
            raise BudgetExhausted("Budget exhausted before anatomy")
        if self.pr_data is None:
            raise RuntimeError("PR data not initialized")

        result_raw = await anatomy_phase(
            pr_data=self.pr_data.model_dump(),
            intake=intake.model_dump(),
            repo_path=self.input.repo_path or "",
        )
        self.agent_invocations += 1
        self._register_cost("anatomy", self._extract_cost(result_raw))
        anatomy = AnatomyResult.model_validate(result_raw)
        return anatomy

    async def _run_planning(self, intake: IntakeResult, anatomy: AnatomyResult, review_depth: str) -> ReviewPlan:
        if self._budget_or_timeout_exhausted("planning"):
            raise BudgetExhausted("Budget exhausted before planning")

        result_raw = await planning_phase(
            intake=intake.model_dump(),
            anatomy=anatomy.model_dump(),
            depth=review_depth,
            hints=self.config.hints,
        )
        self.agent_invocations += 1
        self._register_cost("planning", self._extract_cost(result_raw))
        plan = ReviewPlan.model_validate(result_raw)

        depth_profile = DEPTH_PROFILES.get(review_depth)
        if depth_profile and len(plan.dimensions) > depth_profile.max_dimensions:
            plan = plan.model_copy(update={"dimensions": plan.dimensions[: depth_profile.max_dimensions]})

        return plan

    async def _run_parallel_review(
        self,
        plan: ReviewPlan,
        findings_queue: asyncio.Queue[list[ReviewFinding] | None],
        current_depth: int = 0,
    ) -> None:
        max_depth = self.config.budget.max_review_depth
        semaphore = asyncio.Semaphore(self.config.budget.max_concurrent_reviewers)

        async def run_dimension(dim: ReviewDimension, depth: int) -> None:
            if self._budget_or_timeout_exhausted("review"):
                return
            async with semaphore:
                result_raw = await review_dimension(
                    review_prompt=dim.review_prompt,
                    target_files=dim.target_files,
                    context_files=dim.context_files,
                    repo_path=self.input.repo_path or "",
                    current_depth=depth,
                    max_depth=max_depth,
                )
                self.agent_invocations += 1
                self._register_cost("review", self._extract_cost(result_raw))
                findings = self._extract_findings(result_raw, dim)
                await findings_queue.put(findings)

                sub_reviews = self._extract_sub_reviews(result_raw, dim)
                if sub_reviews and depth < max_depth and not self._budget_or_timeout_exhausted("review"):
                    print(
                        f"[PR-AF] Dimension '{dim.name}' spawned {len(sub_reviews)} sub-review(s) at depth {depth + 1}/{max_depth}",
                        flush=True,
                    )
                    sub_tasks = [run_dimension(sub_dim, depth + 1) for sub_dim in sub_reviews]
                    await asyncio.gather(*sub_tasks)

        try:
            tasks = [run_dimension(dim, current_depth) for dim in plan.dimensions]
            if tasks:
                await asyncio.gather(*tasks)
        finally:
            await findings_queue.put(None)

    def _extract_sub_reviews(self, result_raw: object, parent_dim: ReviewDimension) -> list[ReviewDimension]:
        payload = _unwrap(result_raw)
        if not isinstance(payload, dict):
            return []
        raw_subs = payload.get("sub_reviews", [])
        if not isinstance(raw_subs, list):
            return []
        dims: list[ReviewDimension] = []
        for idx, sub in enumerate(raw_subs[:2]):
            if not isinstance(sub, dict):
                continue
            prompt = sub.get("review_prompt", "")
            targets = sub.get("target_files", [])
            if not prompt or not targets:
                continue
            dims.append(
                ReviewDimension(
                    id=f"{parent_dim.id}_sub{idx}",
                    name=f"{parent_dim.name} → {sub.get('reason', 'deep-dive')[:40]}",
                    review_prompt=prompt,
                    target_files=targets,
                    context_files=sub.get("context_files", []),
                    priority=parent_dim.priority,
                )
            )
        return dims

    async def _run_review_layer(
        self,
        plan: ReviewPlan,
        findings_queue: asyncio.Queue[list[ReviewFinding] | None],
        anatomy: AnatomyResult,
    ) -> tuple[list[ReviewFinding], list[CrossRefInteraction], list[AdversaryResult]]:
        all_findings: list[ReviewFinding] = []
        while True:
            batch = await findings_queue.get()
            if batch is None:
                break
            all_findings.extend(batch)

        cross_refs: list[CrossRefInteraction] = []
        adversary_results: list[AdversaryResult] = []

        if all_findings and not self._budget_or_timeout_exhausted("cross_ref"):
            cross_raw = await cross_ref_phase(
                findings=[f.model_dump() for f in all_findings],
                cross_ref_hints=plan.cross_ref_hints,
            )
            self.agent_invocations += 1
            self._register_cost("cross_ref", self._extract_cost(cross_raw))
            cross_refs = self._extract_cross_refs(cross_raw)

        if all_findings and not self._budget_or_timeout_exhausted("adversary"):
            adversary_raw = await adversary_phase(
                findings=[f.model_dump() for f in all_findings],
                ai_generated_confidence=self.intake_result.ai_generated if self.intake_result else 0.0,
            )
            self.agent_invocations += 1
            self._register_cost("adversary", self._extract_cost(adversary_raw))
            adversary_results = self._extract_adversary_results(adversary_raw)

        return all_findings, cross_refs, adversary_results

    async def _run_coverage_loop(
        self,
        plan: ReviewPlan,
        anatomy: AnatomyResult,
        findings: list[ReviewFinding],
        cross_refs: list[CrossRefInteraction],
        adversary_results: list[AdversaryResult],
    ) -> tuple[list[ReviewFinding], list[CrossRefInteraction], list[AdversaryResult]]:
        for _ in range(self.config.budget.max_coverage_iterations):
            if self._budget_or_timeout_exhausted("coverage"):
                break

            reviewed_clusters = self._reviewed_clusters(anatomy, findings)
            gate_raw = await coverage_gate(
                anatomy=anatomy.model_dump(),
                reviewed_clusters=reviewed_clusters,
            )
            self.agent_invocations += 1
            self._register_cost("coverage", self._extract_cost(gate_raw))
            gate = gate_raw if isinstance(gate_raw, dict) else {}
            fully_covered = bool(gate.get("fully_covered", False))
            confident = bool(gate.get("confident", True))
            gap_descriptions = cast(list[str], gate.get("gap_descriptions", []))
            self.coverage_iterations += 1

            if fully_covered or not confident or not gap_descriptions:
                break

            gap_dims = self._build_gap_dimensions(
                anatomy=anatomy,
                gap_descriptions=gap_descriptions,
                reviewed_clusters=reviewed_clusters,
            )
            if not gap_dims:
                break

            gap_queue: asyncio.Queue[list[ReviewFinding] | None] = asyncio.Queue()
            await self._run_parallel_review(
                plan=ReviewPlan(dimensions=gap_dims, cross_ref_hints=plan.cross_ref_hints),
                findings_queue=gap_queue,
            )
            while True:
                batch = await gap_queue.get()
                if batch is None:
                    break
                findings.extend(batch)

            if findings and not self._budget_or_timeout_exhausted("cross_ref"):
                cross_raw = await cross_ref_phase(
                    findings=[f.model_dump() for f in findings],
                    cross_ref_hints=plan.cross_ref_hints,
                )
                self.agent_invocations += 1
                self._register_cost("cross_ref", self._extract_cost(cross_raw))
                cross_refs = self._extract_cross_refs(cross_raw)

            if findings and not self._budget_or_timeout_exhausted("adversary"):
                adversary_raw = await adversary_phase(
                    findings=[f.model_dump() for f in findings],
                    ai_generated_confidence=self.intake_result.ai_generated if self.intake_result else 0.0,
                )
                self.agent_invocations += 1
                self._register_cost("adversary", self._extract_cost(adversary_raw))
                adversary_results = self._extract_adversary_results(adversary_raw)

        return findings, cross_refs, adversary_results

    def _synthesize(
        self,
        findings: list[ReviewFinding],
        cross_refs: list[CrossRefInteraction],
        adversary_results: list[AdversaryResult],
    ) -> list[ScoredFinding]:
        deduped = deduplicate_exact(findings)
        scored = score_findings(
            findings=deduped,
            cross_refs=cross_refs,
            adversary_results=adversary_results,
            config=self.config.scoring,
            ai_generated=self.intake_result.ai_generated if self.intake_result else 0.0,
            blast_radius_size=len(self.anatomy_result.blast_radius) if self.anatomy_result else 0,
        )
        return scored[: self.config.comments.max_comments]

    def _normalize_path(self, path: str) -> str:
        if not path:
            return path
        repo_path = self.input.repo_path or ""
        if repo_path and path.startswith(repo_path):
            path = path[len(repo_path) :].lstrip("/")
        if path.startswith("/workspaces/"):
            parts = path.split("/", 3)
            if len(parts) >= 4:
                path = parts[3]
        return path

    def _diff_line_ranges(self) -> dict[str, list[tuple[int, int]]]:
        if not self.pr_data:
            return {}
        ranges: dict[str, list[tuple[int, int]]] = {}
        for cf in self.pr_data.changed_files:
            if not cf.patch:
                ranges[cf.path] = [(1, 999999)]
                continue
            file_ranges: list[tuple[int, int]] = []
            for line in cf.patch.split("\n"):
                if line.startswith("@@"):
                    import re

                    match = re.search(r"\+(\d+)(?:,(\d+))?", line)
                    if match:
                        start = int(match.group(1))
                        count = int(match.group(2) or "1")
                        file_ranges.append((start, start + count))
            if file_ranges:
                ranges[cf.path] = file_ranges
            else:
                ranges[cf.path] = [(1, 999999)]
        return ranges

    async def _generate_output(
        self,
        scored_findings: list[ScoredFinding],
        intake: IntakeResult,
        anatomy: AnatomyResult,
        plan: ReviewPlan,
    ) -> ReviewResult:
        if self.pr_data is None:
            raise RuntimeError("PR data not initialized")

        diff_files = {cf.path for cf in self.pr_data.changed_files}
        diff_ranges = self._diff_line_ranges()

        severity_rank = {"nitpick": 0, "suggestion": 1, "important": 2, "critical": 3}
        min_rank = severity_rank.get(self.config.comments.min_severity, 1)

        comments: list[GitHubComment] = []
        filtered_for_comments: list[ScoredFinding] = []
        for finding in scored_findings:
            if severity_rank.get(finding.severity, 0) < min_rank:
                continue
            filtered_for_comments.append(finding)
            norm_path = self._normalize_path(finding.file_path)
            if not norm_path or norm_path not in diff_files or finding.line_start <= 0:
                continue
            ranges = diff_ranges.get(norm_path, [])
            in_range = any(start <= finding.line_start <= end for start, end in ranges)
            if not in_range:
                continue
            comments.append(
                GitHubComment(
                    path=norm_path,
                    line=finding.line_start,
                    side=finding.diff_side,
                    body=self._format_comment_body(finding),
                )
            )

        comments = comments[: self.config.comments.max_comments]
        review_event = determine_review_event(filtered_for_comments)

        summary_body = self._format_summary(
            findings=filtered_for_comments,
            review_event=review_event,
        )

        review = GitHubReview(
            body=summary_body,
            event=review_event,
            comments=comments,
        )

        if not self.input.dry_run and self.input.pr_url:
            client = GitHubClient()
            try:
                await client.post_review(
                    owner=self.pr_data.owner,
                    repo=self.pr_data.repo,
                    pr_number=self.pr_data.number,
                    review=review,
                    commit_sha=self.pr_data.head_sha,
                )
                print(
                    f"[PR-AF] Posted review to {self.pr_data.owner}/{self.pr_data.repo}#{self.pr_data.number}",
                    flush=True,
                )
            except httpx.HTTPStatusError as exc:
                # GitHub returns 422 when requesting changes on own PR — retry with COMMENT
                if exc.response.status_code == 422 and "own pull request" in exc.response.text.lower():
                    print("[PR-AF] Cannot request changes on own PR, retrying with COMMENT event", flush=True)
                    review_fallback = GitHubReview(
                        body=summary_body,
                        event="COMMENT",
                        comments=comments,
                    )
                    try:
                        await client.post_review(
                            owner=self.pr_data.owner,
                            repo=self.pr_data.repo,
                            pr_number=self.pr_data.number,
                            review=review_fallback,
                            commit_sha=self.pr_data.head_sha,
                        )
                        print(
                            f"[PR-AF] Posted review (COMMENT) to {self.pr_data.owner}/{self.pr_data.repo}#{self.pr_data.number}",
                            flush=True,
                        )
                    except Exception as retry_exc:
                        print(f"[PR-AF] Failed to post review on retry: {retry_exc}", flush=True)
                else:
                    print(f"[PR-AF] Failed to post review: {exc}", flush=True)
            except Exception as exc:
                print(f"[PR-AF] Failed to post review: {exc}", flush=True)

        by_severity: dict[str, int] = {}
        for finding in scored_findings:
            by_severity[finding.severity] = by_severity.get(finding.severity, 0) + 1

        summary = ReviewSummary(
            total_findings=len(scored_findings),
            by_severity=by_severity,
            dimensions_run=len(plan.dimensions),
            cross_ref_interactions=self.cross_ref_count,
            adversary_challenged=self.adversary_challenged_count,
            adversary_confirmed=self.adversary_confirmed_count,
            coverage_iterations=self.coverage_iterations,
            ai_generated_confidence=intake.ai_generated,
            cost_usd=round(self.total_cost_usd, 4),
            duration_seconds=round(time.monotonic() - self.started_at, 3),
            budget_exhausted=self.budget_exhausted,
        )

        metadata = ReviewMetadata(
            intake=intake.model_dump(),
            anatomy=anatomy.model_dump(),
            plan=plan.model_dump(),
            budget={
                "total_cost_usd": self.total_cost_usd,
                "cost_breakdown": self.cost_breakdown,
                "budget_exhausted": self.budget_exhausted,
                "max_cost_usd": self.config.budget.max_cost_usd,
                "max_duration_seconds": self.config.budget.max_duration_seconds,
            },
            agent_invocations=self.agent_invocations,
            phases_completed=list(self.PHASE_ORDER),
        )

        return ReviewResult(
            review_id=self.review_id,
            pr_url=self.input.pr_url or "",
            review=review,
            findings=scored_findings,
            summary=summary,
            metadata=metadata,
        )

    def _budget_or_timeout_exhausted(self, phase: str) -> bool:
        elapsed = time.monotonic() - self.started_at
        if elapsed > self.config.budget.max_duration_seconds:
            self.budget_exhausted = True
            return True
        if self.total_cost_usd >= self.config.budget.max_cost_usd:
            self.budget_exhausted = True
            return True
        phase_spent = self.cost_breakdown.get(phase, 0.0)
        phase_cap = self.config.budget.phase_budgets.get(phase, float("inf"))
        if phase_spent >= phase_cap:
            return True
        return False

    def _register_cost(self, phase: str, cost: float | None) -> None:
        if cost is None:
            return
        self.total_cost_usd += cost
        self.cost_breakdown[phase] = self.cost_breakdown.get(phase, 0.0) + cost

    def _resolve_depth(self, intake: IntakeResult) -> str:
        """Resolve review depth from intake or auto-detect from PR size."""
        if self.input.depth != "auto":
            return self.input.depth

        if intake.review_depth in DEPTH_PROFILES:
            return intake.review_depth

        if self.pr_data and self.pr_data.diff:
            line_count = len(self.pr_data.diff.splitlines())
            if line_count < min(AUTO_DEPTH_THRESHOLDS):
                return AUTO_DEPTH_THRESHOLDS[min(AUTO_DEPTH_THRESHOLDS)]
            for threshold, depth in sorted(AUTO_DEPTH_THRESHOLDS.items()):
                if line_count < threshold:
                    return depth
            return "deep"

        return "standard"

    def _extract_cost(self, result_raw: object) -> float | None:
        if isinstance(result_raw, dict):
            cost = result_raw.get("cost_usd")
            if isinstance(cost, (int, float)):
                return float(cost)
            payload = _unwrap(result_raw)
            if isinstance(payload, dict):
                inner_cost = payload.get("cost_usd")
                if isinstance(inner_cost, (int, float)):
                    return float(inner_cost)
        return None

    def _extract_findings(self, result_raw: object, dim: ReviewDimension) -> list[ReviewFinding]:
        payload = _unwrap(result_raw)
        findings_raw: list[dict[str, Any]]
        if isinstance(payload, dict):
            if isinstance(payload.get("findings"), list):
                findings_raw = cast(list[dict[str, Any]], payload["findings"])
            elif isinstance(payload.get("results"), list):
                findings_raw = cast(list[dict[str, Any]], payload["results"])
            else:
                findings_raw = []
        elif isinstance(payload, list):
            findings_raw = cast(list[dict[str, Any]], payload)
        else:
            findings_raw = []

        findings: list[ReviewFinding] = []
        for item in findings_raw:
            if not isinstance(item, dict):
                continue
            normalized = {
                "dimension_id": item.get("dimension_id", dim.id),
                "dimension_name": item.get("dimension_name", dim.name),
                "file_path": item.get("file_path", ""),
                "line_start": int(item.get("line_start", 0) or 0),
                "line_end": int(item.get("line_end", 0) or 0),
                "hunk_context": item.get("hunk_context", ""),
                "severity": item.get("severity", "suggestion"),
                "title": item.get("title", "Untitled finding"),
                "body": item.get("body", ""),
                "suggestion": item.get("suggestion"),
                "evidence": item.get("evidence", ""),
                "confidence": float(item.get("confidence", 0.5) or 0.5),
                "tags": item.get("tags", []),
            }
            findings.append(ReviewFinding.model_validate(normalized))

        return findings

    def _extract_cross_refs(self, result_raw: object) -> list[CrossRefInteraction]:
        payload = _unwrap(result_raw)
        raw_list: list[dict[str, Any]] = []
        if isinstance(payload, dict):
            for key in ("interactions", "cross_refs", "results"):
                value = payload.get(key)
                if isinstance(value, list):
                    raw_list = cast(list[dict[str, Any]], value)
                    break
        elif isinstance(payload, list):
            raw_list = cast(list[dict[str, Any]], payload)
        return [CrossRefInteraction.model_validate(item) for item in raw_list]

    def _extract_adversary_results(self, result_raw: object) -> list[AdversaryResult]:
        payload = _unwrap(result_raw)
        raw_list: list[dict[str, Any]] = []
        if isinstance(payload, dict):
            for key in ("results", "adversary_results", "findings"):
                value = payload.get(key)
                if isinstance(value, list):
                    raw_list = cast(list[dict[str, Any]], value)
                    break
        elif isinstance(payload, list):
            raw_list = cast(list[dict[str, Any]], payload)
        return [AdversaryResult.model_validate(item) for item in raw_list]

    def _reviewed_clusters(self, anatomy: AnatomyResult, findings: list[ReviewFinding]) -> list[str]:
        reviewed: set[str] = set()
        finding_paths = {f.file_path for f in findings if f.file_path}
        for cluster in anatomy.clusters:
            if any(path in finding_paths for path in cluster.files):
                reviewed.add(cluster.id)
        return sorted(reviewed)

    def _build_gap_dimensions(
        self,
        anatomy: AnatomyResult,
        gap_descriptions: list[str],
        reviewed_clusters: list[str],
    ) -> list[ReviewDimension]:
        reviewed = set(reviewed_clusters)
        candidate_clusters = [c for c in anatomy.clusters if c.id not in reviewed]
        if not candidate_clusters:
            return []

        dimensions: list[ReviewDimension] = []
        for idx, gap in enumerate(gap_descriptions):
            if idx >= len(candidate_clusters):
                break
            cluster = candidate_clusters[idx]
            prompt = (
                "Coverage gap follow-up review. "
                f"Focus area: {gap}. "
                f"Inspect files: {', '.join(cluster.files)}. "
                "Return only concrete findings with file path, line range, and confidence."
            )
            dimensions.append(
                ReviewDimension(
                    id=f"coverage_gap_{idx}",
                    name=f"Coverage Gap {idx + 1}",
                    review_prompt=prompt,
                    target_files=cluster.files,
                    context_files=[],
                    priority=1,
                )
            )
        return dimensions

    def _format_comment_body(self, finding: ScoredFinding) -> str:
        emoji = self.config.comments.severity_emojis.get(finding.severity, "")
        lines = [f"{emoji} **{finding.title}**".strip(), "", finding.body]

        if self.config.comments.include_suggestions and finding.suggestion:
            lines.extend(["", "Suggested fix:", "```suggestion", finding.suggestion, "```"])

        if finding.evidence:
            lines.extend(["", f"Evidence: {finding.evidence}"])

        meta_parts: list[str] = []
        if self.config.comments.include_dimension_attribution:
            meta_parts.append(f"Dimension: {finding.dimension_name}")
        if self.config.comments.include_confidence:
            meta_parts.append(f"Confidence: {finding.confidence:.2f}")
        if meta_parts:
            lines.extend(["", " | ".join(meta_parts)])

        return "\n".join(lines).strip()

    def _format_summary(self, findings: list[ScoredFinding], review_event: str) -> str:
        by_severity: dict[str, int] = {"critical": 0, "important": 0, "suggestion": 0, "nitpick": 0}
        for finding in findings:
            by_severity[finding.severity] = by_severity.get(finding.severity, 0) + 1

        return "\n".join(
            [
                "## PR-AF Review Summary",
                "",
                f"Review decision: **{review_event}**",
                f"- Total findings: {len(findings)}",
                f"- Critical: {by_severity.get('critical', 0)}",
                f"- Important: {by_severity.get('important', 0)}",
                f"- Suggestions: {by_severity.get('suggestion', 0)}",
                f"- Nitpicks: {by_severity.get('nitpick', 0)}",
            ]
        )

    def _to_changed_file(self, file_change: Any) -> ChangedFile:
        return ChangedFile(
            path=file_change.path,
            status=file_change.status,
            additions=file_change.lines_added,
            deletions=file_change.lines_removed,
            patch="\n\n".join(h.content for h in file_change.hunks),
        )

    def _compute_repo_diff(
        self,
        repo_path: str,
        base_ref: str | None,
        head_ref: str | None,
    ) -> str:
        if head_ref and not base_ref:
            base_ref = "HEAD"
        if base_ref and head_ref:
            revision = f"{base_ref}...{head_ref}"
        elif base_ref:
            revision = f"{base_ref}...HEAD"
        else:
            revision = "HEAD~1...HEAD"

        cmd = ["git", "-C", repo_path, "diff", "--no-color", revision]
        result = subprocess.run(cmd, check=False, capture_output=True, text=True)
        if result.returncode != 0:
            raise ValueError(result.stderr.strip() or "Failed to compute git diff")
        return result.stdout
