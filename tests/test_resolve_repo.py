"""Behavior tests for PR-head checkout into the pr-af workspace.

Maps to the validation contract for ``_checkout_pr_branch``:

* fresh workspace -> working tree reflects the PR head
* REUSED workspace (``pr-review`` already checked out from a prior PR) ->
  working tree updates to the *new* PR head, not left on the prior PR's tree.
  This is the regression test for the silent "no findings" bug: a reused
  workspace kept reviewing every PR after the first against the first PR's tree.
* unfetchable PR ref -> raises, instead of silently leaving a stale tree.

Uses real temporary git repositories (a local path acts as the ``origin``
remote with ``refs/pull/<n>/head`` refs); no network and no mocking of the
function's internals.
"""

from __future__ import annotations

import subprocess
from typing import TYPE_CHECKING

import pytest

from pr_af.app import _checkout_pr_branch

if TYPE_CHECKING:
    from pathlib import Path


def _git(*args: str, cwd: Path) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, check=True, capture_output=True, text=True
    ).stdout.strip()


def _make_upstream(path: Path) -> None:
    path.mkdir()
    _git("init", "-q", "-b", "main", cwd=path)
    _git("config", "user.email", "test@example.com", cwd=path)
    _git("config", "user.name", "test", cwd=path)
    _git("config", "commit.gpgsign", "false", cwd=path)
    (path / "marker.txt").write_text("main\n")
    _git("add", "-A", cwd=path)
    _git("commit", "-qm", "initial", cwd=path)


def _publish_pr_ref(upstream: Path, pr_number: int, content: str) -> None:
    """Create ``refs/pull/<n>/head`` in ``upstream`` with marker.txt=content."""
    _git("checkout", "-q", "-b", f"_pr{pr_number}", "main", cwd=upstream)
    (upstream / "marker.txt").write_text(content + "\n")
    _git("commit", "-qam", f"pr{pr_number}", cwd=upstream)
    sha = _git("rev-parse", "HEAD", cwd=upstream)
    _git("update-ref", f"refs/pull/{pr_number}/head", sha, cwd=upstream)
    _git("checkout", "-q", "main", cwd=upstream)
    _git("branch", "-qD", f"_pr{pr_number}", cwd=upstream)


def _clone_workspace(upstream: Path, target: Path) -> None:
    """Mirror pr-af's clone: shallow, no tags, no default-branch checkout."""
    _git(
        "clone", "--depth", "1", "--no-tags", "--no-checkout",
        str(upstream), str(target), cwd=upstream.parent,
    )


def test_checkout_reused_workspace_updates_to_new_pr_head(tmp_path: Path) -> None:
    """A reused workspace must review the *current* PR, not the previous one."""
    upstream = tmp_path / "upstream"
    _make_upstream(upstream)
    _publish_pr_ref(upstream, 1, "pr1")

    target = tmp_path / "workspace"
    _clone_workspace(upstream, target)
    marker = target / "marker.txt"

    # First PR in this workspace: working tree reflects PR #1.
    _checkout_pr_branch(str(target), 1)
    assert marker.read_text() == "pr1\n"
    assert _git("rev-parse", "--abbrev-ref", "HEAD", cwd=target) == "pr-review"

    # A second PR arrives; the workspace is reused (pr-review still checked out).
    _publish_pr_ref(upstream, 2, "pr2")
    _git("fetch", "--all", cwd=target)  # what _resolve_repo does for a reused dir

    _checkout_pr_branch(str(target), 2)
    # Regression: before the fix this stayed on PR #1's tree ("pr1").
    assert marker.read_text() == "pr2\n"


def test_checkout_fresh_workspace_reflects_pr_head(tmp_path: Path) -> None:
    upstream = tmp_path / "upstream"
    _make_upstream(upstream)
    _publish_pr_ref(upstream, 7, "pr7")

    target = tmp_path / "workspace"
    _clone_workspace(upstream, target)

    _checkout_pr_branch(str(target), 7)
    assert (target / "marker.txt").read_text() == "pr7\n"


def test_checkout_raises_on_unfetchable_pr_ref(tmp_path: Path) -> None:
    upstream = tmp_path / "upstream"
    _make_upstream(upstream)

    target = tmp_path / "workspace"
    _clone_workspace(upstream, target)

    with pytest.raises(ValueError, match="PR #999"):
        _checkout_pr_branch(str(target), 999)
