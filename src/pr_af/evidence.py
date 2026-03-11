from __future__ import annotations

import asyncio
import os
import re
import subprocess
from collections import OrderedDict
from typing import TYPE_CHECKING

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from .schemas.pipeline import ReviewFinding

_SKIP_DIRS = (".git", "node_modules", "__pycache__", ".venv", "vendor", "venv")
_TEXT_EXTENSIONS = {
    ".py",
    ".js",
    ".jsx",
    ".ts",
    ".tsx",
    ".go",
    ".rs",
    ".java",
    ".rb",
    ".php",
    ".c",
    ".h",
    ".cpp",
    ".hpp",
    ".cs",
    ".swift",
    ".kt",
    ".scala",
    ".sh",
    ".yaml",
    ".yml",
    ".json",
    ".toml",
    ".ini",
    ".cfg",
    ".md",
    ".sql",
    ".html",
    ".css",
    ".scss",
    ".txt",
}
_COMMON_IDENTIFIER_WORDS = {
    "the",
    "this",
    "that",
    "with",
    "from",
    "when",
    "where",
    "which",
    "there",
    "their",
    "returns",
    "return",
    "found",
    "check",
    "line",
    "file",
    "code",
    "issue",
    "error",
    "value",
    "values",
    "class",
    "function",
    "method",
    "should",
    "could",
    "would",
    "into",
    "over",
    "under",
    "each",
    "name",
    "data",
    "test",
    "tests",
}


class EvidencePackage(BaseModel):
    """Ground-truth code evidence for a single finding."""

    finding_title: str
    primary_code: str = ""
    caller_snippets: list[str] = Field(default_factory=list)
    cross_ref_snippets: list[str] = Field(default_factory=list)
    diff_hunk: str = ""
    import_context: str = ""
    related_code: str = ""


async def extract_evidence_for_findings(
    findings: list[ReviewFinding],
    repo_path: str,
    diff_patches: dict[str, str],
    blast_radius: list[str] | None = None,
) -> dict[str, EvidencePackage]:
    """Extract ground-truth code evidence for each finding. Returns {finding_title: EvidencePackage}."""
    if not findings:
        return {}

    semaphore = asyncio.Semaphore(10)
    blast_files = blast_radius or []

    async def _extract_for_finding(finding: ReviewFinding) -> EvidencePackage:
        async with semaphore:
            normalized_file = _normalize_relative_path(repo_path, finding.file_path)
            text_blob = "\n".join([finding.title, finding.body, finding.evidence])
            identifiers = _extract_mentioned_identifiers(text_blob)

            primary_task = asyncio.to_thread(
                _read_code_snippet,
                repo_path,
                normalized_file,
                finding.line_start,
                30,
            )
            diff_task = asyncio.to_thread(
                _extract_diff_hunk,
                diff_patches,
                normalized_file,
                finding.line_start,
            )
            import_task = asyncio.to_thread(_build_import_context, repo_path, normalized_file)
            mentioned_files_task = asyncio.to_thread(
                _extract_mentioned_file_paths,
                text_blob,
                repo_path,
            )

            caller_tasks = [
                asyncio.to_thread(_find_function_callers, repo_path, ident, normalized_file)
                for ident in identifiers
            ]
            related_task = asyncio.to_thread(
                _extract_blast_radius_code,
                repo_path,
                normalized_file,
                identifiers,
                blast_files,
            )

            primary_code, diff_hunk, import_context, mentioned_files, related_code = await asyncio.gather(
                primary_task,
                diff_task,
                import_task,
                mentioned_files_task,
                related_task,
            )

            caller_results: list[list[str]] = []
            if caller_tasks:
                caller_results = await asyncio.gather(*caller_tasks)
            caller_snippets = _dedupe_strings([snippet for group in caller_results for snippet in group])
            caller_snippets = caller_snippets[:10]

            cross_ref_tasks = [
                asyncio.to_thread(_read_code_snippet, repo_path, path, 1, 30)
                for path in mentioned_files[:10]
            ]
            cross_ref_results: list[str] = []
            if cross_ref_tasks:
                cross_ref_results = await asyncio.gather(*cross_ref_tasks)
            cross_ref_snippets = _dedupe_strings([item for item in cross_ref_results if item])

            return EvidencePackage(
                finding_title=finding.title,
                primary_code=primary_code,
                caller_snippets=caller_snippets,
                cross_ref_snippets=cross_ref_snippets,
                diff_hunk=diff_hunk,
                import_context=import_context,
                related_code=related_code,
            )

    packages = await asyncio.gather(*[_extract_for_finding(finding) for finding in findings])
    return {package.finding_title: package for package in packages}


def _read_code_snippet(repo_path: str, file_path: str, line: int, context_lines: int = 30) -> str:
    """Read ±context_lines around the given line from the file."""
    normalized = _normalize_relative_path(repo_path, file_path)
    abs_path = os.path.join(repo_path, normalized)
    if not _is_text_file(abs_path):
        return ""

    try:
        with open(abs_path, encoding="utf-8", errors="ignore") as handle:
            lines = handle.readlines()
    except OSError:
        return ""

    if not lines:
        return ""

    target_line = max(1, line)
    start_idx = max(0, target_line - 1 - context_lines)
    end_idx = min(len(lines), target_line + context_lines)

    snippet_lines: list[str] = []
    for idx in range(start_idx, end_idx):
        snippet_lines.append(f"{idx + 1}: {lines[idx].rstrip()}")

    return "\n".join(snippet_lines)


def _find_function_callers(repo_path: str, function_name: str, exclude_file: str = "") -> list[str]:
    """Find call sites for a function across the repository."""
    ident = function_name.strip()
    if not ident or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", ident):
        return []

    pattern = r"\b" + re.escape(ident) + r"\s*\("
    command = [
        "grep",
        "-RInE",
        pattern,
        ".",
        "--exclude-dir=.git",
        "--exclude-dir=node_modules",
        "--exclude-dir=__pycache__",
        "--exclude-dir=.venv",
        "--exclude-dir=vendor",
        "--exclude-dir=venv",
    ]

    try:
        result = subprocess.run(
            command,
            cwd=repo_path,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return []

    normalized_exclude = _normalize_relative_path(repo_path, exclude_file)
    snippets: list[str] = []

    for raw_line in result.stdout.splitlines():
        parts = raw_line.split(":", 2)
        if len(parts) < 3:
            continue
        rel_path = _normalize_relative_path(repo_path, parts[0])
        if rel_path == normalized_exclude:
            continue
        if not _is_text_file(os.path.join(repo_path, rel_path)):
            continue
        try:
            line_no = int(parts[1])
        except ValueError:
            continue

        snippet = _read_code_snippet(repo_path, rel_path, line_no, context_lines=5)
        if snippet:
            header = f"{rel_path}:{line_no}"
            snippets.append(header + "\n" + snippet)
        if len(snippets) >= 10:
            break

    return _dedupe_strings(snippets)


def _extract_mentioned_identifiers(text: str) -> list[str]:
    """Extract likely function/class identifiers mentioned in free-form text."""
    candidates: list[str] = []

    for match in re.findall(r"`([A-Za-z_][A-Za-z0-9_]*)`", text):
        candidates.append(match)
    for match in re.findall(r"\b([A-Z][a-zA-Z0-9]{2,})\b", text):
        candidates.append(match)
    for match in re.findall(r"\b([a-z_][a-z0-9_]{2,})\s*\(", text):
        candidates.append(match)

    deduped: OrderedDict[str, None] = OrderedDict()
    for raw in candidates:
        name = raw.strip("` ")
        if len(name) < 3:
            continue
        if name.lower() in _COMMON_IDENTIFIER_WORDS:
            continue
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
            continue
        deduped[name] = None

    return list(deduped.keys())


def _extract_mentioned_file_paths(text: str, repo_path: str) -> list[str]:
    """Extract valid repository file paths mentioned in text."""
    candidates: set[str] = set()

    backtick_paths = re.findall(r"`([^`]*?/[^`]*?)`", text)
    path_like = re.findall(r"([A-Za-z0-9_./-]+\.[A-Za-z0-9]+)", text)

    for value in backtick_paths + path_like:
        if "/" not in value:
            continue
        if " " in value:
            continue
        normalized = _normalize_relative_path(repo_path, value)
        abs_path = os.path.join(repo_path, normalized)
        if os.path.isfile(abs_path):
            candidates.add(normalized)

    return sorted(candidates)


def _extract_diff_hunk(diff_patches: dict[str, str], file_path: str, line: int | None = None) -> str:
    """Extract patch text for a file, optionally narrowed to the matching hunk."""
    normalized = _normalize_patch_key(file_path)
    patch = diff_patches.get(normalized, "")

    if not patch:
        for key, value in diff_patches.items():
            if _normalize_patch_key(key) == normalized:
                patch = value
                break

    if not patch:
        return ""

    patch_lines = patch.splitlines()
    if line is None:
        return "\n".join(patch_lines[:200])

    hunk_lines = _extract_hunk_for_line(patch_lines, line)
    if not hunk_lines:
        return "\n".join(patch_lines[:200])
    return "\n".join(hunk_lines[:200])


def _build_import_context(repo_path: str, file_path: str) -> str:
    """Build import context as imports in file + files importing this module."""
    normalized = _normalize_relative_path(repo_path, file_path)
    abs_path = os.path.join(repo_path, normalized)

    imports: list[str] = []
    if _is_text_file(abs_path):
        try:
            with open(abs_path, encoding="utf-8", errors="ignore") as handle:
                for raw_line in handle:
                    stripped = raw_line.strip()
                    if stripped.startswith("import ") or stripped.startswith("from "):
                        imports.append(stripped)
        except OSError:
            imports = []

    module_name = _path_to_module(normalized)
    imported_by: list[str] = []

    if module_name:
        regex = r"^\s*(?:from\s+" + re.escape(module_name) + r"\b|import\s+" + re.escape(module_name) + r"\b)"
        command = [
            "grep",
            "-RIlE",
            regex,
            ".",
            "--include=*.py",
            "--exclude-dir=.git",
            "--exclude-dir=node_modules",
            "--exclude-dir=__pycache__",
            "--exclude-dir=.venv",
            "--exclude-dir=vendor",
            "--exclude-dir=venv",
        ]
        try:
            result = subprocess.run(
                command,
                cwd=repo_path,
                capture_output=True,
                text=True,
                timeout=10,
                check=False,
            )
            for raw_path in result.stdout.splitlines():
                rel = _normalize_relative_path(repo_path, raw_path)
                if rel != normalized:
                    imported_by.append(rel)
        except (OSError, subprocess.TimeoutExpired):
            imported_by = []

    imports_list = ", ".join(imports[:30]) if imports else "none"
    imported_by_list = ", ".join(sorted(set(imported_by))[:30]) if imported_by else "none"
    return "IMPORTS: " + imports_list + "\nIMPORTED BY: " + imported_by_list


def _extract_blast_radius_code(
    repo_path: str,
    file_path: str,
    identifiers: list[str],
    blast_radius: list[str],
) -> str:
    """Extract snippets from non-PR blast radius files that reference finding identifiers."""
    if not identifiers or not blast_radius:
        return ""

    normalized_target = _normalize_relative_path(repo_path, file_path)
    snippets: list[str] = []

    for candidate in blast_radius:
        normalized = _normalize_relative_path(repo_path, candidate)
        if normalized == normalized_target:
            continue
        abs_path = os.path.join(repo_path, normalized)
        if not _is_text_file(abs_path):
            continue

        try:
            with open(abs_path, encoding="utf-8", errors="ignore") as handle:
                lines = handle.readlines()
        except OSError:
            continue

        if not lines:
            continue

        for ident in identifiers:
            pattern = re.compile(r"\b" + re.escape(ident) + r"\b")
            match_idx = next((i for i, value in enumerate(lines) if pattern.search(value)), None)
            if match_idx is None:
                continue
            snippet = _format_lines_with_numbers(lines, match_idx + 1, 10)
            if snippet:
                snippets.append(normalized + ":" + str(match_idx + 1) + "\n" + snippet)
            break

        if len(snippets) >= 5:
            break

    return "\n\n".join(snippets[:5])


def _normalize_relative_path(repo_path: str, file_path: str) -> str:
    path = (file_path or "").strip().replace("\\", "/")
    if not path:
        return ""

    path = path.replace("/workspaces/", "", 1) if path.startswith("/workspaces/") else path
    if path.startswith("./"):
        path = path[2:]

    repo_abs = os.path.abspath(repo_path) if repo_path else ""
    path_abs = os.path.abspath(path) if os.path.isabs(path) else ""

    if repo_abs and path_abs.startswith(repo_abs):
        path = os.path.relpath(path_abs, repo_abs)
    elif path.startswith("/"):
        path = path.lstrip("/")

    repo_name = os.path.basename(repo_abs)
    marker = repo_name + "/"
    if marker and marker in path:
        marker_index = path.find(marker)
        if marker_index >= 0:
            path = path[marker_index + len(marker) :]

    return os.path.normpath(path).replace("\\", "/")


def _normalize_patch_key(file_path: str) -> str:
    normalized = file_path.replace("\\", "/").strip()
    for prefix in ("a/", "b/"):
        if normalized.startswith(prefix):
            normalized = normalized[len(prefix) :]
    return normalized.lstrip("/")


def _extract_hunk_for_line(patch_lines: list[str], line: int) -> list[str]:
    current_hunk: list[str] = []
    current_start = 0
    current_count = 0

    for raw in patch_lines:
        if raw.startswith("@@"):
            if current_hunk and current_count > 0 and current_start <= line < current_start + current_count:
                return current_hunk
            current_hunk = [raw]
            match = re.match(r"@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", raw)
            if match:
                current_start = int(match.group(1))
                current_count = int(match.group(2) or "1")
            else:
                current_start = 0
                current_count = 0
        elif current_hunk:
            current_hunk.append(raw)

    if current_hunk and current_count > 0 and current_start <= line < current_start + current_count:
        return current_hunk
    return []


def _path_to_module(file_path: str) -> str:
    if not file_path.endswith(".py"):
        return ""
    module = file_path.replace("/", ".")
    if module.endswith(".__init__.py"):
        module = module[: -len(".__init__.py")]
    elif module.endswith(".py"):
        module = module[: -len(".py")]
    return module


def _format_lines_with_numbers(lines: list[str], target_line: int, context_lines: int) -> str:
    if not lines:
        return ""

    start_idx = max(0, target_line - 1 - context_lines)
    end_idx = min(len(lines), target_line + context_lines)
    output: list[str] = []
    for idx in range(start_idx, end_idx):
        output.append(f"{idx + 1}: {lines[idx].rstrip()}")
    return "\n".join(output)


def _dedupe_strings(values: list[str]) -> list[str]:
    seen: OrderedDict[str, None] = OrderedDict()
    for value in values:
        if value:
            seen[value] = None
    return list(seen.keys())


def _is_text_file(path: str) -> bool:
    if not path or not os.path.isfile(path):
        return False

    ext = os.path.splitext(path)[1].lower()
    if ext in _TEXT_EXTENSIONS:
        return True
    if ext:
        return False

    # Extension-less files: quickly scan for null bytes.
    try:
        with open(path, "rb") as handle:
            sample = handle.read(1024)
        return b"\x00" not in sample
    except OSError:
        return False
