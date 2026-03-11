from __future__ import annotations

import os

from pydantic import BaseModel, Field

from ..blast_radius import compute_blast_radius
from ..diff_engine import cluster_changes, compute_diff_stats, parse_unified_diff
from ..schemas.gates import CoverageGate, IntakeGate
from ..schemas.input import GitHubPRData
from ..schemas.pipeline import (
    AdversaryResult,
    AnatomyResult,
    ChangeCluster,
    FileChange,
    IntakeResult,
    MetaDimensionResult,
    ReviewFinding,
    ReviewPlan,
)
from . import router


class _AnatomySemanticResult(BaseModel):
    pr_narrative: str = ""
    risk_surfaces: list[str] = Field(default_factory=list)
    unrelated_changes: list[str] = Field(default_factory=list)
    intent_gaps: list[str] = Field(default_factory=list)
    context_notes: str = ""


class _SubReviewRequest(BaseModel):
    reason: str = ""
    review_prompt: str = ""
    target_files: list[str] = Field(default_factory=list)
    context_files: list[str] = Field(default_factory=list)
    priority: int = 1


class _ReviewFindingsResult(BaseModel):
    findings: list[ReviewFinding] = Field(default_factory=list)
    sub_reviews: list[_SubReviewRequest] = Field(default_factory=list)


class _CompoundFinding(BaseModel):
    title: str = ""
    severity: str = "suggestion"
    file_path: str = ""
    line_start: int = 0
    line_end: int = 0
    body: str = ""
    evidence: str = ""
    suggestion: str | None = None
    confidence: float = 0.5
    tags: list[str] = Field(default_factory=list)
    contributing_findings: list[str] = Field(default_factory=list)


class _CompoundResult(BaseModel):
    findings: list[_CompoundFinding] = Field(default_factory=list)


class _CompoundDedupResult(BaseModel):
    keep_indices: list[int] = Field(default_factory=list)
    reasoning: str = ""


class _AdversaryPhaseResult(BaseModel):
    results: list[AdversaryResult] = Field(default_factory=list)


class _VerifiedFinding(BaseModel):
    title: str = ""
    verified: bool = True
    actual_behavior: str = ""
    revised_severity: str = ""
    revised_confidence: float = 0.5
    verification_notes: str = ""


class _VerificationResult(BaseModel):
    verified_findings: list[_VerifiedFinding] = Field(default_factory=list)


def _auto_depth(complexity: str) -> str:
    mapping = {
        "trivial": "quick",
        "standard": "standard",
        "complex": "deep",
        "massive": "deep",
    }
    return mapping.get(complexity, "standard")


def _language_from_path(path: str) -> str:
    ext_map = {
        ".py": "python",
        ".js": "javascript",
        ".jsx": "javascript",
        ".ts": "typescript",
        ".tsx": "typescript",
        ".go": "go",
        ".rs": "rust",
        ".java": "java",
        ".rb": "ruby",
        ".php": "php",
        ".swift": "swift",
        ".kt": "kotlin",
        ".cs": "csharp",
        ".cpp": "cpp",
        ".c": "c",
        ".sql": "sql",
        ".html": "html",
        ".css": "css",
        ".md": "markdown",
        ".json": "json",
        ".yaml": "yaml",
        ".yml": "yaml",
        ".sh": "bash",
    }
    for ext, language in ext_map.items():
        if path.endswith(ext):
            return language
    return ""


def _extract_languages(pr: GitHubPRData) -> list[str]:
    languages = {_language_from_path(changed.path) for changed in pr.changed_files if _language_from_path(changed.path)}
    return sorted(languages)


def _write_context_file(content: str, name: str, repo_path: str) -> str:
    """Write large context to a file for .harness() to read. Returns file path."""
    ctx_dir = os.path.join(repo_path, ".pr-af-context")
    os.makedirs(ctx_dir, exist_ok=True)
    path = os.path.join(ctx_dir, name)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    return path


def _extract_areas(paths: list[str]) -> list[str]:
    area_patterns = {
        "auth": ("auth", "login", "oauth", "permission", "acl"),
        "database": ("db", "database", "migration", "schema", "model"),
        "api": ("api", "endpoint", "route", "controller", "handler"),
        "frontend": ("ui", "component", "view", "page", "css", "tsx", "jsx"),
        "tests": ("test", "spec", "fixture"),
        "ci": (".github", "workflow", "ci", "pipeline"),
        "config": ("config", "settings", ".env", "yaml", "toml", "json"),
        "infra": ("docker", "k8s", "terraform", "helm", "ansible"),
        "security": ("security", "crypto", "token", "jwt", "secret"),
    }
    lowered = [path.lower() for path in paths]
    detected: list[str] = []
    for area, patterns in area_patterns.items():
        if any(any(pattern in path for pattern in patterns) for path in lowered):
            detected.append(area)
    if not detected:
        detected.append("application")
    return detected


def _risk_signals(pr: GitHubPRData, areas_touched: list[str], files_changed: int) -> list[str]:
    signals: list[str] = []
    if "security" in areas_touched or "auth" in areas_touched:
        signals.append("touches authentication or security-sensitive paths")
    if "database" in areas_touched:
        signals.append("modifies data model or schema-affecting code")
    if "api" in areas_touched:
        signals.append("changes API surface or request/response behavior")
    if files_changed >= 25:
        signals.append("large change footprint across many files")
    if any(path.endswith((".yml", ".yaml", ".toml", ".json")) for path in (cf.path for cf in pr.changed_files)):
        signals.append("includes configuration changes")
    if any("test" in cf.path.lower() for cf in pr.changed_files):
        signals.append("test behavior updated")
    return signals


def _ai_generated_confidence(pr: GitHubPRData) -> float:
    signals = 0
    evidence = 0
    text_blobs = [pr.title, pr.description, *pr.commit_messages]
    patterns = (
        "generated by",
        "co-authored-by: claude",
        "co-authored-by: gpt",
        "ai-assisted",
        "autogenerated",
        "chatgpt",
        "copilot",
        "claude",
        "llm",
    )
    for blob in text_blobs:
        if not blob:
            continue
        evidence += 1
        lower = blob.lower()
        if any(pattern in lower for pattern in patterns):
            signals += 1
    if evidence == 0:
        return 0.0
    return min(1.0, signals / evidence)


def _pr_summary(pr: GitHubPRData) -> str:
    description = (pr.description or "").strip()
    if description:
        return description
    return f"{pr.title}. Files changed: {len(pr.changed_files)}."


def _file_changes_from_metadata(pr: GitHubPRData) -> list[FileChange]:
    return [
        FileChange(
            path=changed.path,
            status=changed.status,
            language=_language_from_path(changed.path),
            lines_added=changed.additions,
            lines_removed=changed.deletions,
            hunks=[],
        )
        for changed in pr.changed_files
    ]


def _cluster_descriptions(clusters: list[ChangeCluster]) -> list[dict[str, object]]:
    return [
        {
            "id": cluster.id,
            "name": cluster.name,
            "description": cluster.description,
            "primary_language": cluster.primary_language,
            "files": cluster.files,
        }
        for cluster in clusters
    ]


@router.reasoner()
async def intake_phase(pr_data: dict, depth: str = "standard") -> dict:
    pr = GitHubPRData.model_validate(pr_data)
    files_changed = len(pr.changed_files)
    languages = _extract_languages(pr)
    import json as _json

    ai_input = _json.dumps(
        {
            "title": pr.title,
            "description": (pr.description or "")[:500],
            "labels": pr.labels,
            "author": pr.author,
            "files_changed": files_changed,
            "languages": languages,
            "commit_messages": pr.commit_messages[:5],
        },
        default=str,
    )

    gate_result = await router.app.ai(
        f"Classify this pull request from metadata and diff footprint.\n\n{ai_input}",
        system="Return pr_type, complexity, and confident only. Use the provided schema.",
        schema=IntakeGate,
    )

    if gate_result.confident:
        paths = [changed.path for changed in pr.changed_files]
        areas_touched = _extract_areas(paths)
        intake_result = IntakeResult(
            pr_type=gate_result.pr_type,
            complexity=gate_result.complexity,
            languages=languages,
            areas_touched=areas_touched,
            risk_signals=_risk_signals(pr, areas_touched, files_changed),
            ai_generated=_ai_generated_confidence(pr),
            review_depth=depth if depth != "auto" else _auto_depth(gate_result.complexity),
            pr_summary=_pr_summary(pr),
        )
        return intake_result.model_dump()

    fallback_input = _json.dumps(
        {
            "pr_title": pr.title,
            "description": (pr.description or "")[:1000],
            "requested_depth": depth,
            "languages": languages,
            "files_changed": files_changed,
        },
        default=str,
    )
    fallback_result = await router.app.harness(
        f"Classify this pull request for a multi-agent review pipeline. "
        f"Downstream reviewers will rely on your classification to decide review depth "
        f"and focus areas, so accuracy matters more than speed.\n\n"
        f"Determine: PR type (feature/bugfix/refactor/docs/config/dependency/test), "
        f"complexity (trivial/standard/complex/massive), areas touched, risk signals, "
        f"AI-generation confidence, and write a technical PR summary that captures the "
        f"actual substance of the change (not just the PR title restated).\n\n{fallback_input}",
        schema=IntakeResult,
    )
    return fallback_result.parsed.model_dump() if fallback_result.parsed else {}


@router.reasoner()
async def anatomy_phase(pr_data: dict, intake: dict, repo_path: str = "") -> dict:
    import json as _json

    pr = GitHubPRData.model_validate(pr_data)
    intake_result = IntakeResult.model_validate(intake)

    files = parse_unified_diff(pr.diff)
    if not files:
        files = _file_changes_from_metadata(pr)

    stats = compute_diff_stats(files)
    clusters = cluster_changes(files)
    changed_paths = [file.path for file in files]
    blast_radius = compute_blast_radius(changed_paths, repo_path)

    context = _json.dumps(
        {
            "intake": {
                "pr_type": intake_result.pr_type,
                "complexity": intake_result.complexity,
                "pr_summary": intake_result.pr_summary,
            },
            "pr_metadata": {"title": pr.title, "description": (pr.description or "")[:500], "labels": pr.labels},
            "clusters": _cluster_descriptions(clusters),
            "stats": stats.model_dump(),
            "blast_radius_count": len(blast_radius),
            "files_changed": [
                {"path": f.path, "status": f.status, "lines_added": f.lines_added, "lines_removed": f.lines_removed}
                for f in files[:30]
            ],
        },
        default=str,
    )
    semantic = await router.app.harness(
        f"You are a senior engineer performing structural analysis of a pull request before "
        f"review dimensions are assigned. Your job is NOT to find bugs yet — it is to deeply "
        f"understand WHAT changed, WHY it changed, and WHERE the risk surfaces are.\n\n"
        f"Think like an architect reviewing a change set:\n\n"
        f"1. **PR Narrative**: Write a clear technical narrative of what this PR actually does "
        f"(not what the PR description says — what the CODE says). Trace the change from "
        f"entry point to effect. If the PR replaces one mechanism with another, describe both "
        f"the old and new mechanisms and where they differ.\n\n"
        f"2. **Risk Surfaces**: Identify areas where this change could break things that are "
        f"NOT obvious from the diff alone. Think about:\n"
        f"   - Callers of changed functions/methods that might pass arguments differently\n"
        f"   - Implicit contracts (ordering, timing, state) that the change might violate\n"
        f"   - Error paths — if the old code handled errors one way, does the new code preserve that?\n"
        f"   - Concurrency: thread safety, shared state, decorator-injected arguments\n"
        f"   - API boundaries: do callers still get what they expect?\n"
        f"   - Configuration/defaults that changed (especially security-sensitive ones)\n\n"
        f"3. **Unrelated Changes**: Flag anything that doesn't belong in this PR's stated intent.\n\n"
        f"4. **Intent Gaps**: Where does the code diverge from what the PR description promises? "
        f"Where is the PR description silent about something the code actually does?\n\n"
        f"Be specific. Name files, functions, and line ranges. A vague risk surface is useless.\n\n"
        f"{context}",
        schema=_AnatomySemanticResult,
        cwd=repo_path or None,
    )

    parsed = semantic.parsed if semantic.parsed else _AnatomySemanticResult()
    anatomy_result = AnatomyResult(
        files=files,
        clusters=clusters,
        blast_radius=blast_radius,
        dependency_graph={},
        stats=stats,
        pr_narrative=parsed.pr_narrative,
        risk_surfaces=parsed.risk_surfaces,
        unrelated_changes=parsed.unrelated_changes,
        intent_gaps=parsed.intent_gaps,
        context_notes=parsed.context_notes,
    )
    return anatomy_result.model_dump()


@router.reasoner()
async def planning_phase(intake: dict, anatomy: dict, depth: str = "standard", hints: list[str] | None = None) -> dict:
    import json as _json

    intake_result = IntakeResult.model_validate(intake)
    anatomy_result = AnatomyResult.model_validate(anatomy)
    planner_hints = hints or []

    context = _json.dumps(
        {
            "intake": {
                "pr_type": intake_result.pr_type,
                "complexity": intake_result.complexity,
                "pr_summary": intake_result.pr_summary,
                "areas_touched": intake_result.areas_touched,
                "risk_signals": intake_result.risk_signals,
            },
            "clusters": _cluster_descriptions(anatomy_result.clusters),
            "risk_surfaces": anatomy_result.risk_surfaces,
            "pr_narrative": anatomy_result.pr_narrative,
            "depth": depth,
            "hints": planner_hints,
            "file_paths": [f.path for f in anatomy_result.files[:30]],
        },
        default=str,
    )
    plan_result = await router.app.harness(
        f"You are a principal engineer designing a review strategy for a pull request. "
        f"Your job is to decompose this PR into review DIMENSIONS — each one a focused, "
        f"independently-executable investigation that another senior engineer will carry out.\n\n"
        f"DO NOT use generic templates like 'security review' or 'performance review'. "
        f"Every dimension must be SPECIFIC to what THIS PR actually changes.\n\n"
        f"## How to Think About Dimensions\n\n"
        f"A dimension is NOT 'check file X for bugs'. A dimension is a specific QUESTION about "
        f"the change that requires reading code to answer. Good dimensions:\n\n"
        f"- 'Does the migration from library A to library B preserve error semantics?' "
        f"(target: the wrapper functions; context: the callers)\n"
        f"- 'Are all callers of method X updated to match its new signature?' "
        f"(target: the callers; context: the method definition)\n"
        f"- 'Does the new default value for config Y break existing deployments?' "
        f"(target: where Y is consumed; context: where Y is defined and documented)\n"
        f"- 'Can the refactored data flow produce states that the old flow could not?' "
        f"(target: state transitions; context: consumers of that state)\n\n"
        f"Bad dimensions: 'Review security', 'Check for bugs', 'Validate tests'\n\n"
        f"## Dimension Categories to Consider\n\n"
        f"Not all will apply — generate ONLY what matters for THIS PR:\n\n"
        f"1. **Behavioral Equivalence**: When code is refactored or a dependency is swapped, "
        f"does the new code behave identically in all paths? Edge cases, error handling, "
        f"return types, side effects, timing.\n\n"
        f"2. **Contract Preservation**: Are function signatures, decorator behaviors, "
        f"serialization formats, and API responses preserved? When a decorator adds an "
        f"implicit parameter, are all call sites (direct AND indirect) updated?\n\n"
        f"3. **Cross-Boundary Consistency**: Changes in module A may violate assumptions "
        f"in module B. Look for shared types, constants, configs, or patterns that appear "
        f"in both changed and unchanged files.\n\n"
        f"4. **Error Propagation & Recovery**: Follow every error path. Does the new code "
        f"catch the same exceptions? Raise the same error types? Preserve error codes? "
        f"Avoid swallowing errors that the old code surfaced?\n\n"
        f"5. **State & Concurrency**: Thread-local storage, shared handles, connection "
        f"lifecycle, resource cleanup. Does the change introduce shared mutable state, "
        f"or change who owns a resource?\n\n"
        f"6. **Data Integrity & Migration**: Schema changes, default value changes, "
        f"format changes. Can old data be read by new code? Can new data be read by "
        f"rollback code?\n\n"
        f"7. **Architectural Coherence**: Does this change follow or violate the codebase's "
        f"established patterns? Does it introduce a new pattern where one already exists? "
        f"Does it create technical debt or resolve it?\n\n"
        f"## Review Prompt Craft\n\n"
        f"Each dimension's `review_prompt` will be given to another engineer who will read "
        f"the actual code. Make it a COMPLETE briefing:\n"
        f"- State exactly what to investigate\n"
        f"- Explain what 'correct' looks like\n"
        f"- Point out what subtle failures would look like\n"
        f"- Mention specific functions, classes, or patterns to trace\n\n"
        f"## Cross-Reference Hints\n\n"
        f"Identify specific pairs or groups of findings that could interact. "
        f"Example: 'If dimension A finds that error types changed, AND dimension B finds "
        f"callers that catch specific error types, those interact.'\n\n"
        f"## Output Requirements\n\n"
        f"- Prioritize dimensions by risk (highest first)\n"
        f"- Each dimension has: target_files (to inspect) and context_files (for reference)\n"
        f"- Depth '{depth}' means: quick=2-3 dimensions, standard=3-5, deep=5-8, thorough=6-10\n"
        f"- If the PR has a narrow scope, fewer dimensions is BETTER than padding with fluff\n\n"
        f"{context}",
        schema=ReviewPlan,
    )
    return plan_result.parsed.model_dump() if plan_result.parsed else {"dimensions": [], "cross_ref_hints": []}


# ---------------------------------------------------------------------------
# Meta-Dimension Selectors (3 parallel lenses)
# Each produces ReviewDimensions through its specific analytical lens.
# The orchestrator spawns all 3 in parallel, collects results, deduplicates.
# ---------------------------------------------------------------------------


def _build_meta_context(intake: dict, anatomy: dict, diff_patches: dict[str, str] | None = None) -> str:
    """Build shared context string for all meta-selectors."""
    import json as _json

    intake_result = IntakeResult.model_validate(intake)
    anatomy_result = AnatomyResult.model_validate(anatomy)

    payload: dict[str, object] = {
        "intake": {
            "pr_type": intake_result.pr_type,
            "complexity": intake_result.complexity,
            "pr_summary": intake_result.pr_summary,
            "areas_touched": intake_result.areas_touched,
            "risk_signals": intake_result.risk_signals,
        },
        "clusters": _cluster_descriptions(anatomy_result.clusters),
        "risk_surfaces": anatomy_result.risk_surfaces,
        "pr_narrative": anatomy_result.pr_narrative,
        "blast_radius": anatomy_result.blast_radius[:20],
        "intent_gaps": anatomy_result.intent_gaps,
        "unrelated_changes": anatomy_result.unrelated_changes,
        "context_notes": anatomy_result.context_notes,
        "diff_stats": {
            "total_files": anatomy_result.stats.total_files,
            "total_additions": anatomy_result.stats.total_additions,
            "total_deletions": anatomy_result.stats.total_deletions,
        },
        "file_paths": [f.path for f in anatomy_result.files[:30]],
    }

    if diff_patches:
        payload["diff_patches"] = dict(list(diff_patches.items())[:15])

    return _json.dumps(payload, default=str)


@router.reasoner()
async def meta_semantic(
    intake: dict,
    anatomy: dict,
    depth: str = "standard",
    repo_path: str = "",
    diff_patches: dict[str, str] | None = None,
) -> dict:
    """Semantic lens: What does this code DO differently?

    Focuses on logic, behavior, API contracts, concurrency, security, error handling.
    Asks: "If I run the old code and the new code side by side, where do they diverge?"
    """
    context = _build_meta_context(intake, anatomy, diff_patches)
    context_ref = f"{context}"
    if repo_path and len(context) > 8000:
        file_path = _write_context_file(context, "meta_semantic_context.json", repo_path)
        context_ref = (
            f"\n\nFull analysis context written to: {file_path}\n"
            f"Read this file for complete PR context including diff patches."
        )

    result = await router.app.harness(
        f"You are a principal engineer designing review dimensions through the SEMANTIC lens.\n\n"
        f"## Your Lens: SEMANTIC — What does this code DO differently?\n\n"
        f"You are responsible for generating review dimensions that investigate the "
        f"BEHAVIORAL and LOGICAL aspects of this change. Think about:\n\n"
        f"- **Logic changes**: Does the new code produce different results than the old code "
        f"for ANY input? Not just the happy path — edge cases, error conditions, boundary values.\n"
        f"- **API contract changes**: Do callers still get what they expect? Return types, "
        f"error types, side effects, ordering guarantees.\n"
        f"- **Concurrency & state**: Thread safety, shared mutable state, lock ordering, "
        f"resource lifecycle changes.\n"
        f"- **Security implications**: Authentication bypass, authorization checks, input "
        f"validation changes, secret handling.\n"
        f"- **Error handling**: Are exceptions caught the same way? Are error codes preserved? "
        f"Are there silent swallows or unhandled paths?\n"
        f"- **Data flow**: Does data pass through the same transformations? Are there type "
        f"coercions, format changes, or encoding differences?\n\n"
        f"## Investigation Protocol\n\n"
        f"You have full access to the repository. The context below gives you a starting "
        f"point — PR summary, anatomy, and diff patches.\n\n"
        f"- START by reading the context to understand WHAT changed.\n"
        f"- THEN browse the actual source files to understand HOW the changed code fits into "
        f"the broader codebase.\n"
        f"- Read the changed functions. Then find their callers. Trace how data flows through "
        f"them. Check what error paths exist.\n"
        f"- ADAPT your investigation based on what you discover — if you find a concerning "
        f"pattern, dig deeper in adjacent files and call paths.\n\n"
        f"## What NOT to Include\n\n"
        f"Do NOT generate dimensions about:\n"
        f"- Code style, naming, formatting (that's Systemic)\n"
        f"- Type signatures, calling conventions, decorator mechanics (that's Mechanical)\n"
        f"- Pattern consistency, architectural fit (that's Systemic)\n\n"
        f"## Dimension Craft\n\n"
        f"Each dimension must be a SPECIFIC investigation question, not a generic category.\n"
        f"Good: 'Does the migration from sync to async preserve error propagation to callers?'\n"
        f"Bad: 'Check for concurrency issues'\n\n"
        f"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), "
        f"target_files, context_files, and priority (higher = more critical).\n"
        f"The review_prompt must include specific file paths and line ranges discovered during "
        f"your repository investigation, plus the exact verification steps the reviewer should run.\n\n"
        f"## Quality Gate\n\n"
        f"Do NOT generate dimensions based solely on diff text. Every dimension must be informed "
        f"by what you discovered in the actual codebase. If your rationale says 'visible in the "
        f"diff' or 'based on the patches', you have not investigated enough.\n\n"
        f"Depth '{depth}' means: quick=1-2 dimensions, standard=2-3, deep=3-5\n"
        f"If the PR has no semantic risk, return ZERO dimensions. Do not pad.\n\n"
        f"Also provide a rationale explaining your dimension choices and a confidence "
        f"score (0-1) for how completely your dimensions cover the semantic risk surface.\n\n"
        f"{context_ref}",
        schema=MetaDimensionResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else MetaDimensionResult(lens="semantic", dimensions=[])
    parsed.lens = "semantic"
    return parsed.model_dump()


@router.reasoner()
async def meta_mechanical(
    intake: dict,
    anatomy: dict,
    depth: str = "standard",
    repo_path: str = "",
    diff_patches: dict[str, str] | None = None,
) -> dict:
    """Mechanical lens: Does this code WORK correctly at the language level?

    Focuses on types, signatures, calling conventions, decorator effects,
    framework interactions. Asks: "Will this code compile/run without errors?"
    """
    context = _build_meta_context(intake, anatomy, diff_patches)
    context_ref = f"{context}"
    if repo_path and len(context) > 8000:
        file_path = _write_context_file(context, "meta_mechanical_context.json", repo_path)
        context_ref = (
            f"\n\nFull analysis context written to: {file_path}\n"
            f"Read this file for complete PR context including diff patches."
        )

    result = await router.app.harness(
        f"You are a principal engineer designing review dimensions through the MECHANICAL lens.\n\n"
        f"## Your Lens: MECHANICAL — Does this code WORK correctly?\n\n"
        f"You are responsible for generating review dimensions that investigate whether "
        f"the code is STRUCTURALLY correct at the language and framework level. Think about:\n\n"
        f"- **Type correctness**: Do function return types match what callers expect? "
        f"Are there implicit type coercions that will fail at runtime? Does `list[dict]` "
        f"flow where `str` is expected?\n"
        f"- **Signature compatibility**: If a function's parameters changed, do ALL callers "
        f"(direct and indirect) still pass the right arguments? Are there default values "
        f"that mask breakage?\n"
        f"- **Decorator/middleware effects**: When a decorator injects parameters (like "
        f"thread-local storage), are all call paths aware? Does calling a method directly "
        f"vs through a dispatcher change what parameters it receives?\n"
        f"- **Framework contract compliance**: Does this code satisfy the framework's "
        f"expectations? Correct method signatures for overrides, proper hook registration, "
        f"required return types for middleware chains.\n"
        f"- **Import/dependency resolution**: Are all imports valid? Are there circular "
        f"dependencies? Are optional dependencies guarded?\n"
        f"- **Runtime mechanics**: Will this code actually execute without AttributeError, "
        f"TypeError, KeyError, ImportError? Trace the exact runtime behavior.\n\n"
        f"## Investigation Protocol\n\n"
        f"You have full access to the repository. The context below gives you a starting "
        f"point — PR summary, anatomy, and diff patches.\n\n"
        f"- START by reading the context to understand WHAT changed.\n"
        f"- THEN browse the actual source files to understand HOW the changed code fits into "
        f"the broader codebase.\n"
        f"- Read the actual function signatures that changed. Then search for all callers of "
        f"those functions. Check whether callers pass the right arguments and whether import "
        f"chains still resolve correctly.\n"
        f"- ADAPT your investigation based on what you discover — if you find one caller or "
        f"dependency break, keep tracing until you understand blast radius.\n\n"
        f"## What NOT to Include\n\n"
        f"Do NOT generate dimensions about:\n"
        f"- Whether the logic is correct (that's Semantic)\n"
        f"- Code quality or patterns (that's Systemic)\n"
        f"- Business logic validation (that's Semantic)\n\n"
        f"## Dimension Craft\n\n"
        f"Each dimension must target a SPECIFIC mechanical concern.\n"
        f"Good: 'Do all callers of `process_item()` pass the new `context` parameter "
        f"added in this PR?'\n"
        f"Bad: 'Check for type errors'\n\n"
        f"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), "
        f"target_files, context_files, and priority (higher = more critical).\n"
        f"The review_prompt must include specific file paths and line ranges discovered during "
        f"your repository investigation, plus the exact call sites/import chains to verify.\n\n"
        f"## Quality Gate\n\n"
        f"Do NOT generate dimensions based solely on diff text. Every dimension must be informed "
        f"by what you discovered in the actual codebase. If your rationale says 'visible in the "
        f"diff' or 'based on the patches', you have not investigated enough.\n\n"
        f"Depth '{depth}' means: quick=1-2 dimensions, standard=2-3, deep=3-5\n"
        f"If the PR has no mechanical risk, return ZERO dimensions. Do not pad.\n\n"
        f"Also provide a rationale explaining your dimension choices and a confidence "
        f"score (0-1) for how completely your dimensions cover the mechanical risk surface.\n\n"
        f"{context_ref}",
        schema=MetaDimensionResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else MetaDimensionResult(lens="mechanical", dimensions=[])
    parsed.lens = "mechanical"
    return parsed.model_dump()


@router.reasoner()
async def meta_systemic(
    intake: dict,
    anatomy: dict,
    depth: str = "standard",
    repo_path: str = "",
    diff_patches: dict[str, str] | None = None,
) -> dict:
    """Systemic lens: How does this code FIT the codebase?

    Focuses on patterns, complexity, readability, architectural coherence,
    test coverage. Asks: "Does this change make the codebase better or worse?"
    """
    context = _build_meta_context(intake, anatomy, diff_patches)
    context_ref = f"{context}"
    if repo_path and len(context) > 8000:
        file_path = _write_context_file(context, "meta_systemic_context.json", repo_path)
        context_ref = (
            f"\n\nFull analysis context written to: {file_path}\n"
            f"Read this file for complete PR context including diff patches."
        )

    result = await router.app.harness(
        f"You are a principal engineer designing review dimensions through the SYSTEMIC lens.\n\n"
        f"## Your Lens: SYSTEMIC — How does this code FIT?\n\n"
        f"You are responsible for generating review dimensions that investigate whether "
        f"this change is ARCHITECTURALLY sound and consistent with the codebase. Think about:\n\n"
        f"- **Pattern consistency**: Does this change follow established patterns in the "
        f"codebase, or does it introduce a new pattern where one already exists? If it "
        f"introduces a new pattern, is it justified?\n"
        f"- **Complexity impact**: Does this change increase cyclomatic complexity? "
        f"Are there deeply nested conditionals, god functions, or tangled dependencies?\n"
        f"- **Abstraction quality**: Are the right things abstracted? Is there unnecessary "
        f"indirection, or conversely, inline code that should be extracted?\n"
        f"- **Test coverage alignment**: Are the changes tested? Do tests cover the "
        f"interesting edge cases, or just the happy path? Are there test patterns that "
        f"should be followed?\n"
        f"- **Documentation debt**: Are public APIs documented? Are complex algorithms "
        f"explained? Are there misleading comments that weren't updated?\n"
        f"- **Dependency hygiene**: Are new dependencies justified? Are there lighter "
        f"alternatives? Is the dependency well-maintained?\n"
        f"- **Migration completeness**: If this is part of a larger migration, is it "
        f"complete or does it leave the codebase in a mixed state?\n\n"
        f"## Investigation Protocol\n\n"
        f"You have full access to the repository. The context below gives you a starting "
        f"point — PR summary, anatomy, and diff patches.\n\n"
        f"- START by reading the context to understand WHAT changed.\n"
        f"- THEN browse the actual source files to understand HOW the changed code fits into "
        f"the broader codebase.\n"
        f"- Browse similar files in the same directories to understand existing patterns and "
        f"compare the changed code against those patterns.\n"
        f"- ADAPT your investigation based on what you discover — if the change deviates from "
        f"an established architecture pattern, trace where else that pattern is enforced.\n\n"
        f"## What NOT to Include\n\n"
        f"Do NOT generate dimensions about:\n"
        f"- Whether the logic produces correct results (that's Semantic)\n"
        f"- Whether the code will run without type/import errors (that's Mechanical)\n"
        f"- Specific bug hunting (that's Semantic/Mechanical)\n\n"
        f"## Dimension Craft\n\n"
        f"Each dimension must target a SPECIFIC systemic concern.\n"
        f"Good: 'Does the new `UserService` class follow the existing service pattern "
        f"(stateless, injected deps, interface-first)?'\n"
        f"Bad: 'Check code quality'\n\n"
        f"Each dimension needs: id, name, review_prompt (complete briefing for the reviewer), "
        f"target_files, context_files, and priority (higher = more critical).\n"
        f"The review_prompt must include specific file paths and line ranges discovered during "
        f"your repository investigation, plus the pattern comparisons the reviewer should validate.\n\n"
        f"## Quality Gate\n\n"
        f"Do NOT generate dimensions based solely on diff text. Every dimension must be informed "
        f"by what you discovered in the actual codebase. If your rationale says 'visible in the "
        f"diff' or 'based on the patches', you have not investigated enough.\n\n"
        f"Depth '{depth}' means: quick=0-1 dimensions, standard=1-2, deep=2-3\n"
        f"Systemic concerns are LOWER priority than Semantic and Mechanical. "
        f"If the PR is a focused bugfix with no architectural impact, return ZERO dimensions.\n\n"
        f"Also provide a rationale explaining your dimension choices and a confidence "
        f"score (0-1) for how completely your dimensions cover the systemic risk surface.\n\n"
        f"{context_ref}",
        schema=MetaDimensionResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else MetaDimensionResult(lens="systemic", dimensions=[])
    parsed.lens = "systemic"
    return parsed.model_dump()


@router.reasoner()
async def review_dimension(
    review_prompt: str,
    target_files: list[str],
    context_files: list[str] | None = None,
    repo_path: str = "",
    current_depth: int = 0,
    max_depth: int = 2,
    pr_narrative: str = "",
    risk_surfaces: list[str] | None = None,
    intake_summary: str = "",
    diff_patches: dict[str, str] | None = None,
    all_dimension_names: list[str] | None = None,
) -> dict:
    ctx_files = context_files or []
    risks = risk_surfaces or []
    can_spawn = current_depth < max_depth

    pr_context_section = ""
    if pr_narrative or risks:
        pr_context_section = (
            "## PR Context\n\n"
            f"PR narrative: {pr_narrative or 'not provided'}\n"
            f"Risk surfaces: {', '.join(risks) if risks else 'none provided'}\n\n"
        )

    intake_section = f"## Intake Summary\n\n{intake_summary}\n\n" if intake_summary else ""

    dimensions_section = (
        "## Other Review Dimensions\n\n"
        f"Other dimensions being reviewed in parallel: {', '.join(all_dimension_names or [])}. "
        "Avoid duplicating findings that clearly belong to another dimension.\n\n"
    )

    diff_section = ""
    if diff_patches:
        relevant_patches = [
            (path, diff_patches[path]) for path in target_files if path in diff_patches and diff_patches[path]
        ]
        if relevant_patches:
            patches_text = "\n\n".join(f"### {path}\n```diff\n{patch}\n```" for path, patch in relevant_patches)
            if repo_path and len(patches_text) > 6000:
                patch_file = _write_context_file(patches_text, "review_dimension_diff_patches.md", repo_path)
                diff_section = (
                    "## Diff Patches for Target Files\n\n"
                    f"Full diff patches written to: {patch_file}\n"
                    "Read this file for detailed target-file patches.\n\n"
                )
            else:
                diff_section = f"## Diff Patches for Target Files\n\n{patches_text}\n\n"

    spawn_instruction = ""
    if can_spawn:
        spawn_instruction = (
            "\n\nSUB-REVIEW SPAWNING: You may request deeper sub-reviews for areas that need "
            "specialized investigation beyond your current scope. Only request a sub-review when:\n"
            "- You found a complex issue that requires reading additional files not in your target list\n"
            "- A finding reveals a pattern that may repeat across other files\n"
            "- You suspect a security/correctness issue but lack context to confirm it\n"
            f"Current depth: {current_depth}/{max_depth}. "
            f"You have {max_depth - current_depth} level(s) of sub-review remaining. "
            "Do NOT request sub-reviews for trivial issues or things you can resolve yourself. "
            "Maximum 2 sub-reviews per dimension."
        )
    else:
        spawn_instruction = (
            "\n\nYou are at maximum review depth. Do NOT request any sub-reviews. "
            "Report all findings directly, even if uncertain."
        )

    prompt = (
        f"You are a senior engineer performing a focused code review. You have been assigned "
        f"a specific review dimension with a clear investigation question.\n\n"
        f"## Your Assignment\n\n"
        f"{review_prompt}\n\n"
        f"**Target files** (read and analyze these): {', '.join(target_files)}\n"
        f"**Context files** (reference as needed): {', '.join(ctx_files) if ctx_files else 'none'}\n\n"
        f"{pr_context_section}"
        f"{intake_section}"
        f"{dimensions_section}"
        f"{diff_section}"
        f"## How to Review\n\n"
        f"You have access to the entire repository. READ the actual files, don't just analyze "
        f"the diff patches. The diff shows you WHAT changed — the repo shows you the FULL "
        f"context of WHY it matters.\n\n"
        f"Do NOT just scan for surface-level issues. Think deeply about what this code DOES:\n\n"
        f"1. **Read the target files thoroughly.** Understand the control flow, data flow, "
        f"and error paths. Pay attention to what happens at boundaries — function entry/exit, "
        f"exception handlers, early returns, decorator effects.\n\n"
        f"2. **Trace implications.** If a function signature changed, who calls it? "
        f"If a default value changed, where is it consumed? If an import was added or removed, "
        f"what depended on it? When checking callers/consumers of changed code, actually search "
        f"the codebase for references and verify call sites in real files.\n\n"
        f"3. **Check behavioral equivalence.** If code was refactored or a library was swapped, "
        f"does the new version handle ALL the same cases? Edge cases matter: empty inputs, "
        f"None values, concurrent access, error conditions, type mismatches.\n\n"
        f"4. **Verify contracts.** Are return types preserved? Are exception types consistent? "
        f"Do decorators inject parameters that callers might not account for? "
        f"Are there implicit ordering dependencies?\n\n"
        f"5. **Think about what's NOT in the diff.** The most dangerous bugs are in code "
        f"that WASN'T changed but SHOULD have been. If a method's signature changed, "
        f"every caller needs updating. If an enum added a variant, every switch/match "
        f"needs the new case.\n\n"
        f"Before reporting a finding, verify your claim against the actual code. Open the file, "
        f"read the function, and confirm the behavior you are claiming exists.\n\n"
        f"## Severity Calibration\n\n"
        f"Use the FULL severity range. A well-calibrated review has a MIX:\n\n"
        f"- **critical**: Runtime crashes, data corruption, security vulnerabilities, "
        f"silent logic errors that produce wrong results. The code WILL fail in production. "
        f"You must be able to describe the EXACT failure scenario — 'X calls Y with Z, "
        f"which causes W'. Vague concerns are not critical.\n"
        f"- **important**: Missing error handling, validation gaps, API contract violations, "
        f"race conditions under realistic load, performance traps with specific data sizes. "
        f"The code CAN fail under known conditions.\n"
        f"- **suggestion**: Better design patterns, improved abstractions, edge cases worth "
        f"handling, test coverage gaps for specific scenarios. The code works but could be "
        f"more robust.\n"
        f"- **nitpick**: Naming, style, readability, documentation. Truly cosmetic.\n\n"
        f"If you're unsure whether something is critical or important, provide your reasoning "
        f"in the `body` field and let the confidence score reflect your uncertainty.\n\n"
        f"## False-Positive Prevention (CRITICAL)\n\n"
        f"Before reporting ANY finding, you MUST pass these three gates:\n\n"
        f"### Gate 1: Reachability Proof\n"
        f"Trace the EXACT call path from a real entry point to the buggy code. "
        f"If you cannot construct a concrete scenario where the bug triggers, "
        f"it is NOT a finding — it is speculation. Ask yourself:\n"
        f"- Can this code path actually be reached in production?\n"
        f"- Are there upstream guards, validators, or type checks that prevent the bad state?\n"
        f"- Is the 'broken' behavior actually intentional (defensive coding, legacy compat)?\n\n"
        f"### Gate 2: Evidence Chain\n"
        f"Every finding MUST have a step-by-step evidence chain in the `evidence` field:\n"
        f"```\n"
        f"Step 1: [Entry point] calls [function] with [specific args]\n"
        f"Step 2: [function] passes [value] to [downstream]\n"
        f"Step 3: [downstream] expects [type/value] but receives [actual]\n"
        f"Step 4: This causes [specific failure mode]\n"
        f"```\n"
        f"If you cannot write this chain, the finding is not well-evidenced enough to report.\n\n"
        f"### Gate 3: Confidence Self-Assessment\n"
        f"Rate your confidence honestly. Only report findings with confidence >= 0.6.\n"
        f"- 0.9-1.0: You traced the full path and verified the failure mode\n"
        f"- 0.7-0.8: Strong evidence but some assumptions about runtime state\n"
        f"- 0.6: Reasonable evidence, worth flagging for human review\n"
        f"- Below 0.6: Do NOT report. You are guessing.\n\n"
        f"**Zero tolerance for speculative findings.** Three well-proven findings are worth "
        f"infinitely more than ten speculative ones. When in doubt, DROP the finding.\n\n"
        f"## Output Quality\n\n"
        f"For each finding, use proper GitHub Markdown:\n"
        f"- **body**: Explain the issue clearly. Use `inline code` for identifiers. "
        f"Use code blocks with language hints for snippets. Bold key terms. "
        f"Explain WHY this is a problem, not just WHAT is wrong.\n"
        f"- **evidence**: Quote the EXACT code or trace the EXACT call path that demonstrates "
        f"the issue. Include function names, parameter bindings, and return values. "
        f"'Step 1: X calls Y with arg=Z. Step 2: Y binds Z to parameter W. Step 3: W.foo() "
        f"fails because Z is a list, not a TLS object.'\n"
        f"- **suggestion**: Describe the fix concisely. What to change, where, and why. "
        f"If there are multiple valid approaches, mention the tradeoffs.\n"
        f"- **file_path**: Full path from the repository root.\n"
        f"- **line_start**: The specific line where the issue manifests. Be precise.\n\n"
        f"Do NOT produce findings you aren't confident about just to fill a quota. "
        f"Three well-evidenced findings are worth more than ten vague ones."
        f"{spawn_instruction}"
    )
    result = await router.app.harness(
        prompt,
        schema=_ReviewFindingsResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else _ReviewFindingsResult()
    sub_review_dicts = []
    if can_spawn and parsed.sub_reviews:
        sub_review_dicts = [
            {
                "reason": sr.reason,
                "review_prompt": sr.review_prompt,
                "target_files": sr.target_files,
                "context_files": sr.context_files,
                "priority": sr.priority,
            }
            for sr in parsed.sub_reviews[:2]
            if sr.review_prompt and sr.target_files
        ]
    return {
        "findings": [finding.model_dump() for finding in parsed.findings],
        "sub_reviews": sub_review_dicts,
        "current_depth": current_depth,
    }


@router.reasoner()
async def compound_finder_phase(
    cluster_findings: list[dict],
    repo_path: str = "",
    evidence_map: dict[str, dict] | None = None,
) -> dict:
    import json as _json

    ev_map = evidence_map or {}
    validated_findings = [ReviewFinding.model_validate(finding) for finding in cluster_findings]
    if len(validated_findings) < 2:
        return {"findings": []}

    cluster_titles = {finding.title for finding in validated_findings}

    findings_with_context: list[dict] = []
    for f in validated_findings[:4]:
        entry: dict = {
            "title": f.title,
            "severity": f.severity,
            "file_path": f.file_path,
            "line_start": f.line_start,
            "line_end": f.line_end,
            "dimension_name": f.dimension_name,
            "body": f.body,
            "evidence": f.evidence,
            "suggestion": f.suggestion,
            "tags": f.tags,
        }
        ev = ev_map.get(f.title, {})
        if ev:
            entry["evidence_package"] = {
                "primary_code": ev.get("primary_code", "")[:4000],
                "import_context": ev.get("import_context", "")[:2500],
                "caller_snippets": ev.get("caller_snippets", [])[:5],
                "related_code": ev.get("related_code", "")[:2500],
                "cross_ref_snippets": ev.get("cross_ref_snippets", [])[:4],
            }
        findings_with_context.append(entry)

    relevant_evidence: dict[str, dict] = {title: ev_map[title] for title in cluster_titles if title in ev_map}
    payload = {
        "cluster_findings": findings_with_context,
        "cluster_evidence": relevant_evidence,
    }
    findings_summary = _json.dumps(payload, default=str)

    if len(findings_summary) > 10000 and repo_path:
        file_path = _write_context_file(findings_summary, "compound_cluster_findings.json", repo_path)
        findings_ref = (
            "Cluster findings and evidence written to: "
            + file_path
            + "\nRead this file for complete compound-analysis context."
        )
    else:
        findings_ref = "Cluster context:\n" + findings_summary

    result = await router.app.harness(
        "You are a compound-risk investigator for PR findings. You are given a SMALL cluster "
        "of findings that might interact. Your task is to investigate whether these findings "
        "combine into something worse than each finding alone, then synthesize NEW first-class "
        "findings when that combined risk is real.\n\n"
        "Use repository access to verify interactions. Treat this as hypothesis-driven analysis, "
        "not pattern matching: investigate whether there is a real chain or shared mechanism that "
        "creates an issue an individual reviewer would likely miss.\n\n"
        "Guidance for investigation depth:\n"
        "- Check whether one finding creates a precondition that enables another.\n"
        "- Check whether separately minor issues create an escalation path together.\n"
        "- Check whether a safety mechanism exists in one place but is disconnected elsewhere.\n"
        "- Check whether fixing one issue can worsen behavior exposed by another.\n"
        "- Check whether repeated patterns indicate a systemic control gap.\n\n"
        "Output contract:\n"
        "- If no credible compound issue exists, return an empty findings list.\n"
        "- If a compound issue exists, emit NEW findings only. Do not repeat original findings.\n"
        "- Each output finding must include: title, severity, file_path, line_start, line_end, "
        "body, evidence, suggestion, confidence, tags, and contributing_findings.\n"
        "- `contributing_findings` must list the exact titles from this cluster that combine.\n"
        "- Only emit findings with confidence >= 0.6 and concrete evidence.\n\n"
        + findings_ref
        + "\n\nReturn strict JSON matching the schema.",
        schema=_CompoundResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else _CompoundResult()
    return {"findings": [finding.model_dump() for finding in parsed.findings]}


@router.reasoner()
async def compound_dedup_phase(
    compound_findings: list[dict],
    individual_findings_summary: str = "",
) -> dict:
    """Deduplicate compound findings via a single harness call.

    The harness receives all compound findings and determines which are
    genuinely unique insights vs near-duplicates covering the same ground.
    Returns the 0-based indices of findings to KEEP.
    """

    if len(compound_findings) <= 1:
        return {"keep_indices": list(range(len(compound_findings))), "reasoning": "single finding, no dedup needed"}

    numbered_findings: list[str] = []
    for idx, f in enumerate(compound_findings):
        numbered_findings.append(
            f"[{idx}] Title: {f.get('title', '')}\n"
            f"    Severity: {f.get('severity', '')}\n"
            f"    File: {f.get('file_path', '')}\n"
            f"    Tags: {f.get('tags', [])}\n"
            f"    Body: {f.get('body', '')[:500]}\n"
            f"    Evidence: {f.get('evidence', '')[:300]}"
        )

    findings_text = "\n\n".join(numbered_findings)

    individual_context = ""
    if individual_findings_summary:
        individual_context = (
            "\n\nFor reference, these are the INDIVIDUAL findings that the compound "
            "findings were synthesized from:\n" + individual_findings_summary
        )

    result = await router.app.harness(
        "You are a deduplication specialist reviewing compound findings from a PR review.\n\n"
        "Compound findings are synthesized from clusters of individual findings. Because "
        "clusters are analyzed independently and in parallel, different clusters sometimes "
        "produce findings that cover the SAME underlying insight from slightly different "
        "angles.\n\n"
        "Your task: identify which compound findings represent genuinely DISTINCT insights "
        "and which are near-duplicates. Two findings are duplicates when they describe the "
        "same root cause, same attack vector, or same systemic pattern — even if phrased "
        "differently or using different terminology.\n\n"
        "When duplicates exist, keep the finding that is:\n"
        "- Most specific and actionable\n"
        "- Best evidenced\n"
        "- Highest severity\n\n"
        "Also check: does any compound finding merely RESTATE what an individual finding "
        "already says without adding a genuinely new cross-cutting insight? If so, drop it.\n\n"
        f"COMPOUND FINDINGS TO EVALUATE ({len(compound_findings)} total):\n\n"
        + findings_text
        + individual_context
        + "\n\nReturn `keep_indices` as a list of 0-based indices of findings to KEEP. "
        "Include your reasoning.",
        schema=_CompoundDedupResult,
    )
    parsed = result.parsed if result.parsed else _CompoundDedupResult()

    # Validate indices are in range
    valid_indices = [i for i in parsed.keep_indices if 0 <= i < len(compound_findings)]
    if not valid_indices:
        # Fallback: keep all if harness returned nothing valid
        valid_indices = list(range(len(compound_findings)))

    return {"keep_indices": valid_indices, "reasoning": parsed.reasoning}


@router.reasoner()
async def evidence_verifier(
    findings: list[dict],
    evidence_packages: dict[str, dict] | None = None,
    pr_context: str = "",
    repo_path: str = "",
) -> dict:
    import json as _json

    validated_findings = [ReviewFinding.model_validate(f) for f in findings]
    ev_map = evidence_packages or {}

    findings_payload: list[dict] = []
    for f in validated_findings:
        entry: dict = {
            "title": f.title,
            "severity": f.severity,
            "file_path": f.file_path,
            "line_start": f.line_start,
            "dimension_name": f.dimension_name,
            "body": f.body,
            "evidence": f.evidence,
            "confidence": f.confidence,
        }
        ev = ev_map.get(f.title, {})
        if ev:
            entry["extracted_code"] = {
                "primary_code": ev.get("primary_code", "")[:4000],
                "caller_snippets": ev.get("caller_snippets", [])[:5],
                "diff_hunk": ev.get("diff_hunk", "")[:2000],
                "import_context": ev.get("import_context", ""),
                "related_code": ev.get("related_code", "")[:2000],
                "cross_ref_snippets": ev.get("cross_ref_snippets", [])[:3],
            }
        findings_payload.append(entry)

    findings_text = _json.dumps(findings_payload, default=str)

    if len(findings_text) > 12000 and repo_path:
        file_path = _write_context_file(findings_text, "verification_findings.json", repo_path)
        findings_ref = (
            "Findings with extracted code written to: " + file_path + "\n"
            "Read this file for the full list of findings and their extracted code context."
        )
    else:
        findings_ref = findings_text

    result = await router.app.harness(
        "You are a senior engineer performing independent verification of code review findings "
        "before they reach the adversarial challenge phase. Each finding below was produced by "
        "a reviewer who read the repository, and each includes `extracted_code` — real source "
        "code pulled programmatically from the repo around the finding location.\n\n"
        "## Your Role\n\n"
        "You are not the original reviewer, and you are not the adversary. You are an "
        "independent investigator. Your job is to determine what the code ACTUALLY does "
        "at each finding location, and whether the reviewer's claim about the code's "
        "behavior is factually accurate.\n\n"
        "## How to Investigate\n\n"
        "For each finding, you have two sources of truth:\n\n"
        "1. **`extracted_code`** — actual source code around the finding location, call sites "
        "of mentioned functions, the diff patch, and import/dependency context. This was "
        "extracted programmatically, so it is what the code really says.\n\n"
        "2. **The repository itself** — you have full access. Use it to trace connections "
        "the extracted code doesn't cover: follow function calls across modules, check how "
        "values flow through layers, understand the broader architecture around the finding.\n\n"
        "Start with the extracted code to understand the local picture. Then browse the repo "
        "to understand the broader context — how does this code connect to the rest of the "
        "system? What are the upstream callers and downstream consumers? What are the implicit "
        "contracts this code participates in?\n\n"
        "## What to Determine\n\n"
        "For each finding, answer these questions through investigation:\n\n"
        "- **Does the code actually behave as the reviewer claims?** Read the `extracted_code` "
        "and compare it against the reviewer's description in `body`. If the reviewer says "
        "'this function uses string comparison' but the extracted code shows `errors.Is()`, "
        "the claim is factually wrong.\n\n"
        "- **Is the described scenario actually reachable?** Check `caller_snippets` and "
        "browse the repo for call paths. Can the problematic state the reviewer describes "
        "actually occur in practice? Are there guards, validators, or type constraints "
        "upstream that prevent it?\n\n"
        "- **What does the broader context reveal?** The `import_context` and `related_code` "
        "show how this file connects to the rest of the codebase. Sometimes a finding looks "
        "valid in isolation but is prevented by code in another module. Sometimes it looks "
        "minor in isolation but is amplified by how the code is used elsewhere.\n\n"
        "- **Is the severity proportionate?** Based on what you found, does the severity "
        "match the actual impact? A 'critical' finding should have a concrete, traceable "
        "failure path. An 'important' finding should have a realistic scenario.\n\n"
        "## Output\n\n"
        "For each finding, return:\n"
        "- `title`: the finding's title (must match exactly)\n"
        "- `verified`: true if the code behavior matches the reviewer's claim, false if it doesn't\n"
        "- `actual_behavior`: what the code ACTUALLY does at this location (brief, factual)\n"
        "- `revised_severity`: your assessment of the correct severity (critical/important/suggestion/nitpick)\n"
        "- `revised_confidence`: your confidence in the finding's validity (0.0-1.0)\n"
        "- `verification_notes`: what you found during investigation that the downstream "
        "adversary should know — especially any discrepancies between the claim and reality, "
        "or important context from the broader codebase\n\n"
        + ("## PR Context\n\n" + pr_context + "\n\n" if pr_context else "")
        + "## Findings to Verify\n\n"
        + findings_ref,
        schema=_VerificationResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else _VerificationResult()
    return {"verified_findings": [vf.model_dump() for vf in parsed.verified_findings]}


@router.reasoner()
async def adversary_phase(
    findings: list[dict],
    ai_generated_confidence: float = 0.0,
    pr_context: str = "",
    repo_path: str = "",
    evidence_packages: dict[str, dict] | None = None,
) -> dict:
    import json as _json

    validated_findings = [ReviewFinding.model_validate(finding) for finding in findings]
    skepticism = "standard"
    if ai_generated_confidence > 0.5:
        skepticism = "high"

    ev_map = evidence_packages or {}

    findings_with_evidence: list[dict] = []
    for f in validated_findings[:20]:
        entry: dict = {
            "title": f.title,
            "severity": f.severity,
            "file_path": f.file_path,
            "dimension_name": f.dimension_name,
            "body": f.body,
            "evidence": f.evidence,
            "suggestion": f.suggestion,
            "confidence": f.confidence,
        }
        ev = ev_map.get(f.title, {})
        if ev:
            entry["ground_truth"] = {
                "primary_code": ev.get("primary_code", "")[:3000],
                "caller_snippets": ev.get("caller_snippets", [])[:5],
                "diff_hunk": ev.get("diff_hunk", "")[:2000],
                "import_context": ev.get("import_context", ""),
                "related_code": ev.get("related_code", "")[:2000],
            }
        findings_with_evidence.append(entry)

    findings_summary = _json.dumps(findings_with_evidence, default=str)

    if len(findings_summary) > 10000 and repo_path:
        file_path = _write_context_file(findings_summary, "adversary_findings.json", repo_path)
        findings_ref = (
            "Full findings with ground-truth evidence written to: " + file_path + "\n"
            "Read this file for complete finding details and code evidence."
        )
    else:
        findings_ref = "Findings with ground-truth evidence:\n" + findings_summary

    has_evidence = bool(ev_map)

    evidence_instruction = ""
    if has_evidence:
        evidence_instruction = (
            "## Ground-Truth Evidence (CRITICAL)\n\n"
            "Each finding below includes a `ground_truth` section containing ACTUAL CODE "
            "extracted programmatically from the repository. This is the REAL code — not the "
            "reviewer's description of it. Use this as your primary verification source:\n\n"
            "- `primary_code`: The actual source code around the finding location (with line numbers)\n"
            "- `caller_snippets`: Real call sites of functions mentioned in the finding\n"
            "- `diff_hunk`: The actual diff patch for this file\n"
            "- `import_context`: What this file imports and what imports it\n"
            "- `related_code`: Code from non-PR files that interact with the finding\n\n"
            "**VERIFICATION PROTOCOL**: For each finding:\n"
            "1. Read the reviewer's CLAIM about what the code does\n"
            "2. Read the `ground_truth.primary_code` to see what the code ACTUALLY does\n"
            "3. If the claim contradicts the ground truth → CHALLENGE as false positive\n"
            "4. If the claim matches the ground truth → check caller_snippets to verify "
            "the failure scenario is reachable\n"
            "5. You may ALSO browse the repo for additional verification, but the ground "
            "truth should catch most false positives\n\n"
        )
    else:
        evidence_instruction = (
            "## Verification Protocol\n\n"
            "No ground-truth evidence was extracted for these findings. You MUST read the "
            "actual repository files yourself to verify each finding. Open the file mentioned, "
            "read the function, and confirm the behavior the reviewer claims exists.\n\n"
        )

    result = await router.app.harness(
        "You are the adversarial reviewer. Your job is to CHALLENGE every finding and "
        "determine whether it is real or a false positive. You are skeptical by default.\n\n"
        + evidence_instruction
        + "## For Each Finding, Determine:\n\n"
        "1. **Does the ground truth match the claim?** Compare the reviewer's description "
        "against the actual code in `ground_truth.primary_code`. If the reviewer says "
        "'function X uses string comparison' but the actual code uses `errors.Is()`, "
        "that is a false positive — CHALLENGE it immediately.\n\n"
        "2. **Is the failure scenario reachable?** Check `ground_truth.caller_snippets` "
        "to see if the described call path actually exists. Are there guards upstream "
        "that prevent the bad state? Does the calling code handle the condition?\n\n"
        "3. **Is the severity correct?** A 'critical' finding must have a concrete crash "
        "or corruption scenario traceable through the ground truth. If the primary code "
        "shows the issue is handled, downgrade or challenge.\n\n"
        "4. **Cross-file interactions**: Check `ground_truth.related_code` and "
        "`ground_truth.import_context` to understand the broader context. A finding "
        "might look valid in isolation but be prevented by code in another file.\n\n"
        "5. **Hidden traps**: Did the reviewer find a real issue but miss a WORSE "
        "version visible in the ground truth code?\n\n"
        "## Verdicts\n\n"
        "- **confirmed**: The ground truth supports the finding. The claim matches the "
        "actual code. The failure scenario is reachable.\n"
        "- **challenged**: The ground truth contradicts the finding. The actual code "
        "does NOT do what the reviewer claims, OR upstream guards prevent the failure.\n"
        "- **escalated**: The ground truth reveals the issue is WORSE than the reviewer "
        "described.\n\n"
        "Skepticism mode: " + skepticism + "\n"
        "AI-generated confidence: "
        + str(ai_generated_confidence)
        + "\n"
        + (
            "(Higher AI confidence: be MORE skeptical of trivial findings)\n\n"
            if ai_generated_confidence > 0.5
            else "\n"
        )
        + ("## PR Context\n\n" + pr_context + "\n\n" if pr_context else "")
        + findings_ref,
        schema=_AdversaryPhaseResult,
        cwd=repo_path or None,
    )
    parsed = result.parsed if result.parsed else _AdversaryPhaseResult()
    return {"results": [item.model_dump() for item in parsed.results]}


@router.reasoner()
async def coverage_gate(
    anatomy: dict,
    reviewed_clusters: list[str],
    dimension_names_reviewed: list[str] | None = None,
) -> dict:
    import json as _json

    anatomy_result = AnatomyResult.model_validate(anatomy)
    cluster_payload = [
        {
            "id": cluster.id,
            "name": cluster.name,
            "description": cluster.description,
            "files": cluster.files,
        }
        for cluster in anatomy_result.clusters
    ]

    context = _json.dumps(
        {
            "all_clusters": cluster_payload,
            "reviewed_clusters": reviewed_clusters,
            "dimensions_reviewed": dimension_names_reviewed or [],
            "risk_surfaces": anatomy_result.risk_surfaces,
        },
        default=str,
    )
    gate = await router.app.ai(
        f"Determine whether review coverage is complete. "
        f"Compare reviewed cluster identifiers against all change clusters. "
        f"Dimensions already reviewed: {', '.join(dimension_names_reviewed or [])}. "
        f"If gaps exist, return concise gap_descriptions.\n\n{context}",
        system="Analyze the coverage state and return the structured result.",
        schema=CoverageGate,
    )
    return gate.model_dump()
