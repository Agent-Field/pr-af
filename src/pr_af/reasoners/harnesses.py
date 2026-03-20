from __future__ import annotations

import os

from pydantic import BaseModel, Field

from ..blast_radius import compute_blast_radius
from ..diff_engine import cluster_changes, compute_diff_stats, parse_unified_diff
from ..runtime_config import get_ai_kwargs, get_harness_kwargs, get_harness_kwargs_for
from ..schemas.gates import CoverageGate, FindingRelevanceGate, IntakeGate, OutputCalibrationGate
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
    reference_key: str = ""  # e.g. "[F1]" — used for archei-compliant matching
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


def _format_findings_for_llm(
    findings: list[dict],
    evidence_packages: dict[str, dict] | None = None,
) -> str:
    """Format findings as natural language narrative with reference keys.

    Per archei rules: context for another LLM agent should be a string,
    not JSON. This produces a readable narrative with [F1], [F2], etc.
    reference keys for programmatic mapping downstream.
    """
    ev_map = evidence_packages or {}
    lines: list[str] = []

    for idx, f in enumerate(findings):
        ref_key = f"[F{idx + 1}]"
        title = f.get("title", "Untitled")
        severity = f.get("severity", "unknown")
        confidence = f.get("confidence", 0.5)
        file_path = f.get("file_path", "")
        line_start = f.get("line_start", 0)
        body = f.get("body", "")
        evidence = f.get("evidence", "")
        suggestion = f.get("suggestion")
        dimension = f.get("dimension_name", "")

        location = file_path
        if line_start:
            location = f"{file_path}:{line_start}"

        lines.append(f'{ref_key} "{title}" ({severity}, confidence: {confidence})')
        if location:
            lines.append(f"  File: {location}")
        if dimension:
            lines.append(f"  Dimension: {dimension}")
        if body:
            lines.append(f"  Claim: {body}")
        if evidence:
            lines.append(f"  Evidence: {evidence}")
        if suggestion:
            lines.append(f"  Suggestion: {suggestion}")

        ev = ev_map.get(title, {})
        if ev:
            primary_code = ev.get("primary_code", "")
            if primary_code:
                truncated = primary_code[:4000]
                lines.append(f"  Source code at location:\n    {truncated}")
            caller_snippets = ev.get("caller_snippets", [])
            if caller_snippets:
                snippets_text = "; ".join(str(s)[:500] for s in caller_snippets[:5])
                lines.append(f"  Call sites: {snippets_text}")
            diff_hunk = ev.get("diff_hunk", "")
            if diff_hunk:
                lines.append(f"  Diff patch:\n    {diff_hunk[:2000]}")
            import_context = ev.get("import_context", "")
            if import_context:
                lines.append(f"  Import context: {import_context}")
            related_code = ev.get("related_code", "")
            if related_code:
                lines.append(f"  Related code: {related_code[:2000]}")
            cross_ref = ev.get("cross_ref_snippets", [])
            if cross_ref:
                refs_text = "; ".join(str(s)[:500] for s in cross_ref[:3])
                lines.append(f"  Cross-references: {refs_text}")
            # Include verification info if present
            verification = ev.get("verification")
            if verification:
                verified = verification.get("verified", True)
                actual = verification.get("actual_behavior", "")
                notes = verification.get("verification_notes", "")
                status = "verified" if verified else "falsified"
                lines.append(f"  Verification status: {status}")
                if actual:
                    lines.append(f"  Actual behavior: {actual}")
                if notes:
                    lines.append(f"  Verification notes: {notes}")

        lines.append("")  # blank line between findings

    return "\n".join(lines)


def _build_reference_key_map(findings: list[dict]) -> dict[str, str]:
    """Build a mapping from reference keys like [F1] to finding titles.

    Returns: {"[F1]": "Missing error handler...", "[F2]": "Unused param...", ...}
    """
    return {
        f"[F{idx + 1}]": f.get("title", "Untitled")
        for idx, f in enumerate(findings)
    }


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
        **get_ai_kwargs(),
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
        **get_harness_kwargs(),
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
        **get_harness_kwargs_for("anatomy"),
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
        **get_harness_kwargs(),
    )
    return plan_result.parsed.model_dump() if plan_result.parsed else {"dimensions": [], "cross_ref_hints": []}


# ---------------------------------------------------------------------------
# Scout/Strategist Meta-Selectors
#
# Instead of one harness per lens browsing the entire repo serially,
# N scouts browse in parallel (one per cluster), then one strategist
# reasons over their reports to produce MetaDimensionResult.
#
# Data flows follow archei rules:
#   - Scout output: investigation as STRING (strategist LLM consumes it)
#   - Triage gate: flat .ai() schema (2 fields)
#   - Strategist input: all scout narratives concatenated as STRING
#   - Strategist output: MetaDimensionResult (hybrid — same as before)
# ---------------------------------------------------------------------------

# Lens-specific focus descriptions for scout investigation prompts
_LENS_FOCUS = {
    "semantic": (
        "SEMANTIC — What does this code DO differently?\n\n"
        "Investigate the BEHAVIORAL and LOGICAL aspects of changes in this cluster:\n"
        "- Logic changes: Does the new code produce different results for ANY input?\n"
        "- API contract changes: Do callers still get what they expect?\n"
        "- Concurrency & state: Thread safety, shared mutable state, resource lifecycle.\n"
        "- Security implications: Auth bypass, input validation, secret handling.\n"
        "- Error handling: Are exceptions caught the same way? Silent swallows?\n"
        "- Data flow: Type coercions, format changes, encoding differences.\n\n"
        "Do NOT investigate: code style, naming, formatting, type signatures, "
        "architectural patterns (those belong to other lenses)."
    ),
    "mechanical": (
        "MECHANICAL — Does this code WORK correctly at the language level?\n\n"
        "Investigate STRUCTURAL correctness in this cluster:\n"
        "- Type correctness: Do return types match what callers expect?\n"
        "- Signature compatibility: Do ALL callers pass the right arguments?\n"
        "- Decorator/middleware effects: Are all call paths aware of injected params?\n"
        "- Framework contract compliance: Correct method signatures for overrides?\n"
        "- Import/dependency resolution: Valid imports, no circular deps?\n"
        "- Runtime mechanics: Will this code execute without AttributeError, TypeError?\n\n"
        "Do NOT investigate: whether logic is correct, code quality, business logic."
    ),
    "systemic": (
        "SYSTEMIC — Functional risks from architectural choices.\n\n"
        "Investigate FUNCTIONAL RISKS specific to architectural decisions in this cluster:\n"
        "- Cross-module state consistency after changes\n"
        "- Transaction safety across service boundaries\n"
        "- Backward compatibility of changed public APIs\n"
        "- Data integrity through concurrent access paths\n\n"
        "SKIP: naming consistency, test coverage, documentation, code complexity, "
        "dependency freshness. Only generate insights where the architectural concern "
        "could lead to a runtime bug, data loss, security issue, or production incident.\n\n"
        "Do NOT investigate: whether logic produces correct results (Semantic), "
        "whether code will run without type errors (Mechanical)."
    ),
}

# Lens-specific strategist instructions
_LENS_STRATEGIST_FOCUS = {
    "semantic": (
        "Generate review dimensions investigating BEHAVIORAL and LOGICAL aspects. "
        "Good: 'Does the migration from sync to async preserve error propagation?' "
        "Bad: 'Check for concurrency issues'"
    ),
    "mechanical": (
        "Generate review dimensions investigating STRUCTURAL correctness. "
        "Good: 'Do all callers of process_item() pass the new context parameter?' "
        "Bad: 'Check for type errors'"
    ),
    "systemic": (
        "Generate review dimensions investigating FUNCTIONAL RISKS from architectural choices. "
        "Good: 'Cross-module state consistency after migration' "
        "Bad: 'Naming consistency across modules'. "
        "If the PR has no systemic risk, return ZERO dimensions. Prefer zero over noise."
    ),
}


@router.reasoner()
async def cluster_triage(
    cluster_id: str,
    cluster_files: list[str],
    pr_type: str,
    lens: str,
) -> dict:
    """Fast .ai() gate to skip irrelevant clusters before scouting.

    Textbook .ai(): < 200 tokens input, flat 2-field schema, fast classification.
    """
    from ..schemas.gates import ClusterTriageGate

    import json as _json

    context = _json.dumps({
        "cluster_id": cluster_id,
        "files": cluster_files,
        "pr_type": pr_type,
        "lens": lens,
    })

    gate = await router.app.ai(
        f"Should this file cluster be investigated for a {lens} review?\n\n"
        f"A cluster is worth scouting if ANY of its files could contain "
        f"issues relevant to the {lens} lens. Skip only if the cluster is clearly "
        f"irrelevant (e.g., docs-only cluster for a mechanical lens).\n\n"
        f"When in doubt, say worth_scouting=true.\n\n{context}",
        system="Determine if this cluster needs investigation. Err on the side of scouting.",
        schema=ClusterTriageGate,
        **get_ai_kwargs(),
    )

    return gate.model_dump()


@router.reasoner()
async def cluster_scout(
    cluster_id: str,
    cluster_files: list[str],
    lens_focus: str,
    pr_context: str,
    cross_cluster_edges: str,
    repo_path: str = "",
    diff_patches: dict[str, str] | None = None,
) -> dict:
    """Scout .harness() that investigates one cluster through one lens.

    Has tool access to browse repo files, trace callers, read imports.
    Multi-turn — reads X, decides to read Y based on what it finds.

    Output follows archei rules:
    - cluster_id: structured (code groups by this)
    - investigation: STRING (strategist LLM consumes it)
    - confident: structured (code uses for fallback)
    """
    from ..schemas.pipeline import ClusterScoutReport

    diff_section = ""
    if diff_patches:
        relevant = {f: diff_patches[f] for f in cluster_files if f in diff_patches}
        if relevant:
            patches_text = "\n\n".join(
                f"### {path}\n```diff\n{patch}\n```"
                for path, patch in relevant.items()
            )
            if repo_path and len(patches_text) > 6000:
                patch_file = _write_context_file(
                    patches_text, f"scout_{cluster_id}_patches.md", repo_path
                )
                diff_section = (
                    f"\n\n## Diff Patches\n\n"
                    f"Full patches written to: {patch_file}\n"
                    f"Read this file for detailed diff context."
                )
            else:
                diff_section = f"\n\n## Diff Patches\n\n{patches_text}"

    result = await router.app.harness(
        f"You are a code scout investigating a specific cluster of changed files "
        f"through a specific analytical lens.\n\n"
        f"## Your Lens\n\n{lens_focus}\n\n"
        f"## Your Cluster\n\n"
        f"Cluster: {cluster_id}\n"
        f"Files: {', '.join(cluster_files)}\n\n"
        f"## PR Context\n\n{pr_context}\n\n"
        f"## Cross-Cluster Dependencies\n\n{cross_cluster_edges}\n\n"
        f"## Investigation Protocol\n\n"
        f"1. Read each file in your cluster thoroughly.\n"
        f"2. For each changed function/method, trace its callers and consumers.\n"
        f"3. Note risk signals specific to your lens.\n"
        f"4. Follow references to other files when relevant (up to 3 hops).\n"
        f"5. Identify specific areas that would benefit from deeper review.\n\n"
        f"## Output\n\n"
        f"Write a narrative investigation report in the `investigation` field. Include:\n"
        f"- What you found in each file (specific functions, line ranges)\n"
        f"- Risk signals you discovered\n"
        f"- Suggested review dimension ideas with specific investigation questions\n"
        f"- Cross-cluster interactions you noticed\n\n"
        f"Set `confident` to false if you couldn't adequately investigate "
        f"(e.g., couldn't read files, input was too large).\n\n"
        f"Write your investigation as natural language — the strategist who reads "
        f"this is an LLM that reasons over narrative text, not structured JSON."
        f"{diff_section}",
        schema=ClusterScoutReport,
        cwd=repo_path or None,
        **get_harness_kwargs_for("cluster_scout"),
    )

    parsed = result.parsed if result.parsed else ClusterScoutReport(
        cluster_id=cluster_id, investigation="Scout could not produce results.", confident=False
    )
    if not parsed.cluster_id:
        parsed = parsed.model_copy(update={"cluster_id": cluster_id})
    return parsed.model_dump()


@router.reasoner()
async def meta_strategist(
    lens: str,
    scout_reports_narrative: str,
    cross_cluster_edges: str,
    pr_context: str,
    depth: str = "standard",
) -> dict:
    """Strategist .harness() that reasons over scout reports to produce dimensions.

    Why .harness() not .ai():
    - Output is MetaDimensionResult with nested ReviewDimension objects
      containing narrative review_prompt fields — violates .ai()'s flat schema rule
    - Input is all scout reports (~2500+ tokens) — exceeds .ai()'s comfort zone

    Input follows archei rules: all context as STRING (LLM consumes it).
    Output: MetaDimensionResult (hybrid — structured for routing, strings for reviewers).
    """
    strategist_focus = _LENS_STRATEGIST_FOCUS.get(lens, "")

    depth_guides = {
        "quick": "quick=1-2 dimensions",
        "standard": "standard=2-3 dimensions",
        "deep": "deep=3-5 dimensions",
    }
    depth_guide = depth_guides.get(depth, "standard=2-3 dimensions")

    result = await router.app.harness(
        f"You are a principal engineer designing review dimensions through the "
        f"{lens.upper()} lens. You have received investigation reports from scouts "
        f"who each explored a different cluster of changed files.\n\n"
        f"## Your Focus\n\n{strategist_focus}\n\n"
        f"## Scout Investigation Reports\n\n{scout_reports_narrative}\n\n"
        f"## Cross-Cluster Dependencies\n\n{cross_cluster_edges}\n\n"
        f"## PR Context\n\n{pr_context}\n\n"
        f"## Your Task\n\n"
        f"Synthesize the scout reports into review DIMENSIONS. Each dimension is a "
        f"specific investigation question that another senior engineer will carry out "
        f"with full repo access.\n\n"
        f"The scouts did the INVESTIGATION — they read files, traced callers, "
        f"found risk signals. Your job is STRATEGIC: decide what review dimensions "
        f"to generate based on their findings.\n\n"
        f"## Dimension Craft\n\n"
        f"Each dimension must be SPECIFIC, not generic.\n"
        f"Each dimension needs: id, name, review_prompt (complete briefing with "
        f"file paths and line ranges from scout reports), target_files, context_files, "
        f"and priority (higher = more critical).\n\n"
        f"The review_prompt must leverage the specific details scouts discovered — "
        f"function names, line ranges, caller information, risk signals.\n\n"
        f"## Cross-Cluster Synthesis\n\n"
        f"Look for interactions BETWEEN clusters that individual scouts couldn't see. "
        f"If Scout A found a changed function and Scout B found a caller of that "
        f"function, that's a dimension only you can create.\n\n"
        f"## Quality Gate\n\n"
        f"Only generate dimensions backed by scout findings. If scouts found no "
        f"risk in your lens, return ZERO dimensions. Do not pad.\n\n"
        f"Depth: {depth_guide}\n\n"
        f"Also provide a rationale explaining your dimension choices and a confidence "
        f"score (0-1) for how completely your dimensions cover the {lens} risk surface.",
        schema=MetaDimensionResult,
        **get_harness_kwargs_for("meta_strategist"),
    )

    parsed = result.parsed if result.parsed else MetaDimensionResult(
        lens=lens, dimensions=[]
    )
    parsed.lens = lens
    return parsed.model_dump()


@router.reasoner()
async def meta_lens_with_scouts(
    lens: str,
    intake: dict,
    anatomy: dict,
    depth: str = "standard",
    repo_path: str = "",
    diff_patches: dict[str, str] | None = None,
    max_scouts: int = 5,
) -> dict:
    """Orchestrate scouts + strategist for a single lens.

    Replaces the monolithic meta_semantic/meta_mechanical/meta_systemic.
    Flow: triage → parallel scouts → strategist → MetaDimensionResult.
    """
    import asyncio
    import json as _json

    intake_result = IntakeResult.model_validate(intake)
    anatomy_result = AnatomyResult.model_validate(anatomy)

    clusters = anatomy_result.clusters
    if not clusters:
        return MetaDimensionResult(lens=lens, dimensions=[]).model_dump()

    # Build PR context as string (per archei: LLM context → string)
    pr_context = (
        f"PR Type: {intake_result.pr_type}\n"
        f"Complexity: {intake_result.complexity}\n"
        f"Summary: {intake_result.pr_summary}\n"
        f"Risk Signals: {', '.join(intake_result.risk_signals)}\n"
        f"Risk Surfaces: {', '.join(anatomy_result.risk_surfaces)}\n"
        f"PR Narrative: {anatomy_result.pr_narrative}"
    )

    # Compute cross-cluster edges from dependency graph (programmatic, not LLM)
    dep_graph = anatomy_result.dependency_graph or {}
    edge_lines: list[str] = []
    cluster_file_sets = {c.id: set(c.files) for c in clusters}
    for file_path, importers in dep_graph.items():
        file_cluster = None
        for cid, files in cluster_file_sets.items():
            if file_path in files:
                file_cluster = cid
                break
        if not file_cluster:
            continue
        for importer in importers:
            importer_cluster = None
            for cid, files in cluster_file_sets.items():
                if importer in files:
                    importer_cluster = cid
                    break
            if importer_cluster and importer_cluster != file_cluster:
                edge_lines.append(
                    f"- {file_path} ({file_cluster}) ← imported by {importer} ({importer_cluster})"
                )

    cross_cluster_edges = "\n".join(edge_lines) if edge_lines else "No cross-cluster dependencies detected."

    lens_focus = _LENS_FOCUS.get(lens, "")

    # --- Triage: fast .ai() gate per cluster ---
    async def triage_cluster(cluster: ChangeCluster) -> bool:
        result = await cluster_triage(
            cluster_id=cluster.id,
            cluster_files=cluster.files,
            pr_type=intake_result.pr_type,
            lens=lens,
        )
        gate = result if isinstance(result, dict) else {}
        worth = gate.get("worth_scouting", True)
        confident = gate.get("confident", True)
        # If not confident, scout anyway (err on side of investigation)
        return worth or not confident

    triage_results = await asyncio.gather(
        *[triage_cluster(c) for c in clusters]
    )
    scoutable = [c for c, worth in zip(clusters, triage_results) if worth]

    if not scoutable:
        return MetaDimensionResult(
            lens=lens, dimensions=[], confidence=0.9,
            rationale="All clusters triaged as irrelevant for this lens.",
        ).model_dump()

    # --- Parallel scouts (one per cluster, capped) ---
    scout_clusters = scoutable[:max_scouts]

    async def run_scout(cluster: ChangeCluster) -> dict:
        cluster_patches = {
            f: diff_patches[f] for f in cluster.files
            if diff_patches and f in diff_patches
        } if diff_patches else None

        return await cluster_scout(
            cluster_id=cluster.id,
            cluster_files=cluster.files,
            lens_focus=lens_focus,
            pr_context=pr_context,
            cross_cluster_edges=cross_cluster_edges,
            repo_path=repo_path,
            diff_patches=cluster_patches,
        )

    scout_results = await asyncio.gather(
        *[run_scout(c) for c in scout_clusters]
    )

    # --- Build strategist input as narrative string (per archei: LLM context → string) ---
    report_sections: list[str] = []
    for report in scout_results:
        if not isinstance(report, dict):
            continue
        cid = report.get("cluster_id", "unknown")
        investigation = report.get("investigation", "No findings.")
        confident = report.get("confident", True)
        report_sections.append(
            f"### Cluster: {cid} (confident: {confident})\n\n{investigation}"
        )

    scout_reports_narrative = (
        "\n\n---\n\n".join(report_sections)
        if report_sections
        else "No scout reports produced."
    )

    # --- Strategist: synthesize scout reports into dimensions ---
    return await meta_strategist(
        lens=lens,
        scout_reports_narrative=scout_reports_narrative,
        cross_cluster_edges=cross_cluster_edges,
        pr_context=pr_context,
        depth=depth,
    )


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
        **get_harness_kwargs_for("reviewer"),
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
    ev_map = evidence_map or {}
    validated_findings = [ReviewFinding.model_validate(finding) for finding in cluster_findings]
    if len(validated_findings) < 2:
        return {"findings": []}

    cluster_titles = {finding.title for finding in validated_findings}

    relevant_evidence: dict[str, dict] = {title: ev_map[title] for title in cluster_titles if title in ev_map}
    findings_narrative = _format_findings_for_llm(
        [f.model_dump() for f in validated_findings[:4]],
        relevant_evidence,
    )

    if len(findings_narrative) > 10000 and repo_path:
        file_path = _write_context_file(findings_narrative, "compound_cluster_findings.txt", repo_path)
        findings_ref = (
            "Cluster findings and evidence written to: "
            + file_path
            + "\nRead this file for complete compound-analysis context."
        )
    else:
        findings_ref = "Cluster context:\n\n" + findings_narrative

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
        **get_harness_kwargs_for("compound_finder"),
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
        **get_harness_kwargs_for("compound_dedup"),
    )
    parsed = result.parsed if result.parsed else _CompoundDedupResult()

    # Validate indices are in range
    valid_indices = [i for i in parsed.keep_indices if 0 <= i < len(compound_findings)]
    if not valid_indices:
        # Fallback: keep all if harness returned nothing valid
        valid_indices = list(range(len(compound_findings)))

    return {"keep_indices": valid_indices, "reasoning": parsed.reasoning}


@router.reasoner()
async def verify_single_finding(
    finding_narrative: str,
    reference_key: str = "",
    pr_context: str = "",
    repo_path: str = "",
) -> dict:
    """Verify a single finding against the actual source code.

    Each finding gets its own .harness() that browses the repo independently.
    This enables parallel verification of all findings.

    Input follows archei rules: finding_narrative is a string (LLM context),
    reference_key is structured (code uses for mapping).
    """
    result = await router.app.harness(
        "You are a senior engineer performing independent verification of a single "
        "code review finding. Your job is to determine what the code ACTUALLY does "
        "at the finding location and whether the reviewer's claim is factually accurate.\n\n"
        "## How to Investigate\n\n"
        "1. Read the finding narrative below — it includes the reviewer's claim, "
        "evidence, and any inline source code.\n"
        "2. Browse the repository to verify: open the file mentioned, read the "
        "function, trace callers, check if upstream guards prevent the failure.\n"
        "3. Compare the reviewer's CLAIM against what the code ACTUALLY does.\n\n"
        "## What to Determine\n\n"
        "- **Does the code behave as claimed?** If the reviewer says 'function X "
        "doesn't handle exception Y' — does it?\n"
        "- **Is the failure scenario reachable?** Are there guards upstream?\n"
        "- **Is the severity proportionate?** A 'critical' needs a concrete crash path.\n\n"
        "## Output\n\n"
        "Return a single verification result with:\n"
        f"- `reference_key`: \"{reference_key}\"\n"
        "- `title`: the finding's title from the narrative\n"
        "- `verified`: true if the claim matches reality, false if it doesn't\n"
        "- `actual_behavior`: what the code ACTUALLY does (brief, factual)\n"
        "- `revised_severity`: your assessment (critical/important/suggestion/nitpick)\n"
        "- `revised_confidence`: your confidence in the finding (0.0-1.0)\n"
        "- `verification_notes`: key context for the downstream adversary\n\n"
        + ("## PR Context\n\n" + pr_context + "\n\n" if pr_context else "")
        + "## Finding to Verify\n\n"
        + finding_narrative,
        schema=_VerifiedFinding,
        cwd=repo_path or None,
        **get_harness_kwargs_for("verify_single"),
    )
    parsed = result.parsed if result.parsed else _VerifiedFinding()
    vf_dict = parsed.model_dump()
    # Ensure reference_key is set even if the harness didn't return it
    if not vf_dict.get("reference_key"):
        vf_dict["reference_key"] = reference_key
    return vf_dict


@router.reasoner()
async def evidence_verifier(
    findings: list[dict],
    evidence_packages: dict[str, dict] | None = None,
    pr_context: str = "",
    repo_path: str = "",
) -> dict:
    ev_map = evidence_packages or {}

    # Build archei-compliant narrative instead of JSON dump
    findings_narrative = _format_findings_for_llm(findings, ev_map)
    ref_key_map = _build_reference_key_map(findings)

    if len(findings_narrative) > 12000 and repo_path:
        file_path = _write_context_file(
            findings_narrative, "verification_findings.txt", repo_path
        )
        findings_ref = (
            "Findings with extracted code written to: " + file_path + "\n"
            "Read this file for the full list of findings and their code context."
        )
    else:
        findings_ref = findings_narrative

    result = await router.app.harness(
        "You are a senior engineer performing independent verification of code review findings "
        "before they reach the adversarial challenge phase. Each finding below was produced by "
        "a reviewer who read the repository, and each includes extracted source code — real source "
        "code pulled programmatically from the repo around the finding location.\n\n"
        "## Your Role\n\n"
        "You are not the original reviewer, and you are not the adversary. You are an "
        "independent investigator. Your job is to determine what the code ACTUALLY does "
        "at each finding location, and whether the reviewer's claim about the code's "
        "behavior is factually accurate.\n\n"
        "## Finding Reference Keys\n\n"
        "Each finding is labeled with a reference key like [F1], [F2], etc. Use these keys "
        "in your output to identify which finding you are verifying. You may also include "
        "the title for clarity, but the reference_key field is the primary identifier.\n\n"
        "## How to Investigate\n\n"
        "For each finding, you have two sources of truth:\n\n"
        "1. **Extracted source code** — actual source code around the finding location, call sites "
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
        "- **Does the code actually behave as the reviewer claims?** Read the extracted code "
        "and compare it against the reviewer's description in `body`. If the reviewer says "
        "'this function uses string comparison' but the extracted code shows `errors.Is()`, "
        "the claim is factually wrong.\n\n"
        "- **Is the described scenario actually reachable?** Check caller snippets and "
        "browse the repo for call paths. Can the problematic state the reviewer describes "
        "actually occur in practice? Are there guards, validators, or type constraints "
        "upstream that prevent it?\n\n"
        "- **What does the broader context reveal?** The import context and related code "
        "show how this file connects to the rest of the codebase. Sometimes a finding looks "
        "valid in isolation but is prevented by code in another module. Sometimes it looks "
        "minor in isolation but is amplified by how the code is used elsewhere.\n\n"
        "- **Is the severity proportionate?** Based on what you found, does the severity "
        "match the actual impact? A 'critical' finding should have a concrete, traceable "
        "failure path. An 'important' finding should have a realistic scenario.\n\n"
        "## Output\n\n"
        "For each finding, return:\n"
        "- `reference_key`: the finding's reference key (e.g. [F1], [F2])\n"
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
        **get_harness_kwargs_for("verify_single"),
    )
    parsed = result.parsed if result.parsed else _VerificationResult()

    # Resolve reference keys back to titles for backward compatibility
    resolved_findings: list[dict] = []
    for vf in parsed.verified_findings:
        vf_dict = vf.model_dump()
        # If verifier used reference_key but not title, resolve from map
        if vf.reference_key and not vf.title:
            vf_dict["title"] = ref_key_map.get(vf.reference_key, vf.reference_key)
        elif vf.reference_key and vf.title:
            # Both present — keep title as-is (verifier may have matched exactly)
            pass
        resolved_findings.append(vf_dict)

    return {"verified_findings": resolved_findings}


@router.reasoner()
async def adversary_phase(
    findings: list[dict],
    ai_generated_confidence: float = 0.0,
    pr_context: str = "",
    repo_path: str = "",
    evidence_packages: dict[str, dict] | None = None,
) -> dict:
    skepticism = "standard"
    if ai_generated_confidence > 0.5:
        skepticism = "high"

    ev_map = evidence_packages or {}

    # Build archei-compliant narrative instead of JSON dump
    findings_narrative = _format_findings_for_llm(findings, ev_map)
    ref_key_map = _build_reference_key_map(findings)

    if len(findings_narrative) > 10000 and repo_path:
        file_path = _write_context_file(
            findings_narrative, "adversary_findings.txt", repo_path
        )
        findings_ref = (
            "Full findings with ground-truth evidence written to: " + file_path + "\n"
            "Read this file for complete finding details and code evidence."
        )
    else:
        findings_ref = "Findings with ground-truth evidence:\n\n" + findings_narrative

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
        "## Reference Keys\n\n"
        "Each finding is labeled [F1], [F2], etc. Use the reference_key field to identify "
        "which finding you are challenging.\n\n"
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
        **get_harness_kwargs_for("adversary"),
    )
    parsed = result.parsed if result.parsed else _AdversaryPhaseResult()

    # Resolve reference keys back to finding_titles for backward compatibility
    resolved_results: list[dict] = []
    for item in parsed.results:
        item_dict = item.model_dump()
        if item.reference_key and not item.finding_title:
            item_dict["finding_title"] = ref_key_map.get(
                item.reference_key, item.reference_key
            )
        resolved_results.append(item_dict)

    return {"results": resolved_results}


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
        **get_ai_kwargs(),
    )
    return gate.model_dump()


@router.reasoner()
async def anatomy_skip_gate(pr_type: str, complexity: str, files_changed: int, languages: list[str]) -> dict:
    """Fast .ai() gate to skip anatomy semantic analysis for trivial PRs."""
    import json as _json
    from ..schemas.gates import AnatomySkipGate

    context = _json.dumps({
        "pr_type": pr_type,
        "complexity": complexity,
        "files_changed": files_changed,
        "languages": languages,
    })

    gate = await router.app.ai(
        f"Should this PR undergo deep semantic analysis (narrative, risk surfaces, intent gaps)?\n\n"
        f"Skip semantic analysis ONLY for truly trivial PRs: docs-only, config-only, "
        f"single-file renames, dependency bumps with no code changes. "
        f"When in doubt, say needs_semantic_analysis=true.\n\n{context}",
        system="Determine if semantic analysis is needed. Err on the side of analyzing.",
        schema=AnatomySkipGate,
        **get_ai_kwargs(),
    )
    return gate.model_dump()


@router.reasoner()
async def lens_skip_gate(
    lens: str,
    pr_type: str,
    complexity: str,
    areas_touched: list[str],
    risk_surfaces: list[str],
) -> dict:
    """Fast .ai() gate to skip a meta-selector lens when irrelevant."""
    import json as _json
    from ..schemas.gates import LensSkipGate

    context = _json.dumps({
        "lens": lens,
        "pr_type": pr_type,
        "complexity": complexity,
        "areas_touched": areas_touched,
        "risk_surfaces": risk_surfaces,
    })

    gate = await router.app.ai(
        f"Is the {lens} review lens relevant for this PR?\n\n"
        f"Lens descriptions:\n"
        f"- semantic: behavioral/logical changes (logic bugs, API contract changes)\n"
        f"- mechanical: structural correctness (type errors, signature mismatches)\n"
        f"- systemic: architectural risks (cross-module consistency, transaction safety)\n\n"
        f"Skip ONLY if the lens is clearly irrelevant. "
        f"When in doubt, say lens_relevant=true.\n\n{context}",
        system="Determine if this lens is relevant. Err on the side of reviewing.",
        schema=LensSkipGate,
        **get_ai_kwargs(),
    )
    return gate.model_dump()


@router.reasoner()
async def finding_relevance_gate(finding: dict) -> dict:
    """Fast .ai() classifier: is this finding a real functional bug or noise?"""
    import json as _json

    finding_summary = _json.dumps(
        {
            "title": finding.get("title", ""),
            "severity": finding.get("severity", ""),
            "body": finding.get("body", "")[:500],
            "file_path": finding.get("file_path", ""),
            "evidence": finding.get("evidence", "")[:300],
        },
        default=str,
    )

    gate = await router.app.ai(
        f"Classify this code review finding.\n\n"
        f"A 'real_bug' is a finding about functional correctness, security, data integrity, "
        f"or runtime behavior that could cause a production incident.\n\n"
        f"A 'style_preference' is about naming, formatting, code organization, or pattern "
        f"consistency that has no functional impact.\n\n"
        f"A 'design_opinion' is a subjective architectural preference that wouldn't cause bugs.\n\n"
        f"A 'false_positive' is a finding where the described issue doesn't actually exist in "
        f"the code or is based on a misunderstanding.\n\n"
        f"Finding:\n{finding_summary}",
        system="Classify this finding accurately. When in doubt, classify as real_bug.",
        schema=FindingRelevanceGate,
        **get_ai_kwargs(),
    )

    return {
        "category": gate.category,
        "confident": gate.confident,
    }


@router.reasoner()
async def output_calibration_gate(findings: list[dict]) -> dict:
    """Final calibration gate: keep only findings worth posting as PR comments.

    Uses .ai() for small batches (<= 5 findings) and falls back to .harness()
    for larger batches where deeper reasoning over many findings is needed.
    """
    import json as _json

    calibration_prompt = (
        "You are a senior engineer deciding which code review comments to post on a PR.\n\n"
        "Keep ONLY findings about:\n"
        "- Functional correctness bugs\n"
        "- Security vulnerabilities\n"
        "- Data integrity issues\n"
        "- Race conditions or concurrency bugs\n"
        "- Resource leaks\n\n"
        "Drop findings about:\n"
        "- Style preferences, naming conventions\n"
        "- Design opinions without functional impact\n"
        "- Suggestions that are nice-to-have but not bugs\n"
        "- Nitpicks about formatting or documentation\n\n"
        "Return the indices of findings to KEEP.\n\n"
    )

    numbered = []
    for i, f in enumerate(findings):
        numbered.append({
            "index": i,
            "title": f.get("title", ""),
            "severity": f.get("severity", ""),
            "body": f.get("body", "")[:300],
            "file_path": f.get("file_path", ""),
            "score": f.get("score", 0),
        })

    findings_json = _json.dumps(numbered, default=str)

    # .ai() fallback pattern: small input uses fast .ai(), large input
    # uses .harness() for deeper reasoning per archei rules
    if len(findings) <= 5:
        gate = await router.app.ai(
            calibration_prompt + f"Findings:\n{findings_json}",
            system="Select findings to keep. Be selective — only real functional issues.",
            schema=OutputCalibrationGate,
            **get_ai_kwargs(),
        )
        return gate.model_dump()

    # Large batch: use .harness() which can reason more deeply about
    # interactions between many findings and make better keep/drop decisions
    result = await router.app.harness(
        calibration_prompt
        + "There are many findings to evaluate. Consider interactions between findings "
        "— sometimes multiple findings point to the same root cause and only the best "
        "representative should be kept. Also consider whether the density of findings "
        "in a particular file indicates a systemic issue worth calling out separately.\n\n"
        + f"Findings:\n{findings_json}",
        schema=OutputCalibrationGate,
        **get_harness_kwargs_for("compound_dedup"),
    )
    parsed = result.parsed if result.parsed else OutputCalibrationGate(
        keep_indices=list(range(len(findings))), reasoning="fallback: keep all"
    )
    return parsed.model_dump()


class _SemanticDedupResult(BaseModel):
    keep_indices: list[int] = Field(default_factory=list)


@router.reasoner()
async def batch_semantic_dedup(findings: list[dict]) -> list[int]:
    """Single .harness() call to deduplicate semantically similar findings.

    Returns indices of findings to keep.
    """
    import json as _json

    numbered = []
    for i, f in enumerate(findings):
        numbered.append({
            "index": i,
            "title": f.get("title", ""),
            "severity": f.get("severity", ""),
            "file_path": f.get("file_path", ""),
            "line_start": f.get("line_start", 0),
            "body": f.get("body", "")[:200],
        })

    findings_json = _json.dumps(numbered, default=str)

    result = await router.app.harness(
        f"You are deduplicating code review findings. Multiple findings may describe "
        f"the same underlying issue from different angles or in different words.\n\n"
        f"For each group of semantically similar findings (same root cause, same code "
        f"location, or same conceptual issue), keep ONLY the best representative — "
        f"the one with the most specific and actionable description.\n\n"
        f"Two findings are semantically similar if:\n"
        f"- They describe the same bug/issue in different words\n"
        f"- They point to the same root cause but from different code paths\n"
        f"- One is a generalization of the other\n\n"
        f"Return the indices of findings to KEEP (not the ones to drop).\n\n"
        f"Findings:\n{findings_json}",
        schema=_SemanticDedupResult,
        **get_harness_kwargs_for("compound_dedup"),
    )

    parsed = result.parsed if result.parsed else _SemanticDedupResult()
    return parsed.keep_indices
