from __future__ import annotations

import os
import re
from collections import defaultdict


def compute_blast_radius(changed_files: list[str], repo_path: str) -> list[str]:
    """Find files affected by changes but not in the changeset.

    Uses simple import graph: if file A imports file B, and B changed,
    then A is in the blast radius.
    """
    if not repo_path or not os.path.isdir(repo_path):
        return []

    dep_graph = build_import_graph(repo_path)
    changed_set = set(changed_files)
    affected: set[str] = set()

    for changed in changed_files:
        for dependent, imports in dep_graph.items():
            if changed in imports and dependent not in changed_set:
                affected.add(dependent)

    return sorted(affected)


def build_import_graph(repo_path: str) -> dict[str, list[str]]:
    """Build a file-level dependency graph from import statements.

    Returns: {file_path: [files_it_imports]}
    Currently supports Python imports only. Extensible.
    """
    graph: dict[str, list[str]] = defaultdict(list)
    py_files: list[str] = []

    for root, _dirs, files in os.walk(repo_path):
        # Skip common non-source directories
        rel_root = os.path.relpath(root, repo_path)
        if any(
            skip in rel_root
            for skip in (
                ".git",
                "node_modules",
                "vendor",
                "__pycache__",
                ".venv",
                "venv",
            )
        ):
            continue
        for f in files:
            if f.endswith(".py"):
                py_files.append(os.path.join(root, f))

    module_to_file = _build_python_module_map(py_files, repo_path)

    for py_file in py_files:
        rel_path = os.path.relpath(py_file, repo_path)
        try:
            with open(py_file, encoding="utf-8", errors="ignore") as fh:
                content = fh.read()
        except OSError:
            continue

        imported_modules = _extract_python_imports(content)
        for mod in imported_modules:
            if mod in module_to_file:
                graph[rel_path].append(module_to_file[mod])

    return dict(graph)


def _build_python_module_map(py_files: list[str], repo_path: str) -> dict[str, str]:
    """Map Python module names to relative file paths."""
    mapping: dict[str, str] = {}
    for py_file in py_files:
        rel = os.path.relpath(py_file, repo_path)
        module = rel.replace(os.sep, ".").removesuffix(".py").removesuffix(".__init__")
        mapping[module] = rel
    return mapping


def _extract_python_imports(content: str) -> list[str]:
    """Extract imported module names from Python source."""
    modules: list[str] = []
    for match in re.finditer(r"^\s*(?:from|import)\s+([\w.]+)", content, re.MULTILINE):
        modules.append(match.group(1))
    return modules
