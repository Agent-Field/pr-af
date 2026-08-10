"""Tests for ignore_paths filtering at intake (#65).

Validation contract:

* generated/lockfile churn is dropped from the review input before any agent
  sees it — changed_files and the diff both shrink
* a lockfile-regen PR no longer resolves to a deep review: auto-depth is
  computed from the filtered diff
* pattern semantics: ``**/name`` matches top-level and nested, ``dir/**``
  matches the subtree, extension globs match by basename
"""

from __future__ import annotations

from pr_af.config import ReviewConfig
from pr_af.orchestrator import ReviewOrchestrator, _is_ignored_path
from pr_af.schemas.input import ChangedFile, GitHubPRData, ReviewInput
from pr_af.schemas.pipeline import IntakeResult

DEFAULT_PATTERNS = ReviewConfig().ignore_paths


def _pr_data(files: list[ChangedFile]) -> GitHubPRData:
    diff = "\n".join(
        f"diff --git a/{f.path} b/{f.path}\n--- a/{f.path}\n+++ b/{f.path}\n{f.patch}" for f in files
    )
    return GitHubPRData(
        owner="o", repo="r", number=1, title="t", description="", diff=diff, changed_files=files
    )


def _changed(path: str, patch: str) -> ChangedFile:
    return ChangedFile(
        path=path, status="modified", additions=patch.count("\n+"), deletions=0, patch=patch
    )


def _intake(review_depth: str = "") -> IntakeResult:
    return IntakeResult(
        pr_type="feature",
        complexity="standard",
        languages=[],
        areas_touched=[],
        risk_signals=[],
        ai_generated=0.0,
        review_depth=review_depth,
        pr_summary="",
    )


def test_pattern_semantics() -> None:
    assert _is_ignored_path("package-lock.json", DEFAULT_PATTERNS)
    assert _is_ignored_path("web/package-lock.json", DEFAULT_PATTERNS)
    assert _is_ignored_path("yarn.lock", DEFAULT_PATTERNS)
    assert _is_ignored_path(".github/workflows/ci.yml", DEFAULT_PATTERNS)
    assert _is_ignored_path("docs/README.md", DEFAULT_PATTERNS)
    assert _is_ignored_path("vendor/lib/x.go", DEFAULT_PATTERNS)
    assert _is_ignored_path("dist/app.min.js", DEFAULT_PATTERNS)
    assert not _is_ignored_path("src/app.py", DEFAULT_PATTERNS)
    assert not _is_ignored_path("src/lockfile_parser.py", DEFAULT_PATTERNS)


def test_lockfile_regen_is_filtered_before_intake() -> None:
    lock_patch = "@@ -1,3 +1,60000 @@\n" + "\n".join(f'+  "dep-{i}": "1.0.{i}"' for i in range(200))
    src_patch = "@@ -1,2 +1,3 @@\n+import os"
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="d"))
    pr_data = _pr_data([_changed("package-lock.json", lock_patch), _changed("src/app.py", src_patch)])

    filtered = orchestrator._apply_ignore_paths(pr_data)

    assert [f.path for f in filtered.changed_files] == ["src/app.py"]
    assert "dep-0" not in filtered.diff
    assert "import os" in filtered.diff

    orchestrator.pr_data = filtered
    assert orchestrator._resolve_depth(_intake()) == "quick"


def test_lockfile_only_pr_filters_to_empty() -> None:
    lock_patch = "@@ -1 +1,3 @@\n+a\n+b\n+c"
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="d"))
    pr_data = _pr_data([_changed("package-lock.json", lock_patch)])

    filtered = orchestrator._apply_ignore_paths(pr_data)

    assert filtered.changed_files == []
    assert filtered.diff == ""


def test_no_match_returns_input_unchanged() -> None:
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="d"))
    pr_data = _pr_data([_changed("src/app.py", "@@ -1 +1 @@\n+x")])

    assert orchestrator._apply_ignore_paths(pr_data) is pr_data


def test_empty_patterns_disable_filtering() -> None:
    config = ReviewConfig()
    config.ignore_paths = []
    orchestrator = ReviewOrchestrator(app=None, input=ReviewInput(diff_text="d"), config=config)
    pr_data = _pr_data([_changed("package-lock.json", "@@ -1 +1 @@\n+x")])

    assert orchestrator._apply_ignore_paths(pr_data) is pr_data
