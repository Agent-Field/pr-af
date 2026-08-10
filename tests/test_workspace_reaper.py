"""Tests for the stale-workspace reaper (#65).

Validation contract:

* workspaces idle past PR_AF_WORKSPACE_TTL_DAYS are deleted on resolution
* fresh workspaces, the active workspace, and non-directories are never touched
* a recent ``.git/FETCH_HEAD`` counts as activity even when the dir mtime is old
* TTL <= 0 disables reaping
"""

from __future__ import annotations

import os
import time
from pathlib import Path

import pytest

from pr_af.app import _reap_stale_workspaces

WEEK = 8 * 86400


def _backdate(path: Path, seconds: float) -> None:
    stamp = time.time() - seconds
    os.utime(path, (stamp, stamp))


def _make_workspace(workdir: Path, name: str, idle_seconds: float) -> Path:
    ws = workdir / name
    (ws / ".git").mkdir(parents=True)
    _backdate(ws / ".git", idle_seconds)
    _backdate(ws, idle_seconds)
    return ws


def test_stale_workspace_is_reaped(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PR_AF_WORKSPACE_TTL_DAYS", raising=False)
    stale = _make_workspace(tmp_path, "old-repo-pr1", idle_seconds=WEEK)
    fresh = _make_workspace(tmp_path, "new-repo-pr2", idle_seconds=60)

    _reap_stale_workspaces(str(tmp_path))

    assert not stale.exists()
    assert fresh.exists()


def test_active_workspace_is_kept_even_when_stale(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("PR_AF_WORKSPACE_TTL_DAYS", raising=False)
    active = _make_workspace(tmp_path, "repo-pr3", idle_seconds=WEEK)

    _reap_stale_workspaces(str(tmp_path), keep=str(active))

    assert active.exists()


def test_fresh_fetch_head_counts_as_activity(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("PR_AF_WORKSPACE_TTL_DAYS", raising=False)
    ws = _make_workspace(tmp_path, "repo-pr4", idle_seconds=WEEK)
    (ws / ".git" / "FETCH_HEAD").write_text("ref")

    _reap_stale_workspaces(str(tmp_path))

    assert ws.exists()


def test_ttl_zero_disables_reaping(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PR_AF_WORKSPACE_TTL_DAYS", "0")
    stale = _make_workspace(tmp_path, "repo-pr5", idle_seconds=WEEK)

    _reap_stale_workspaces(str(tmp_path))

    assert stale.exists()


def test_plain_files_are_never_touched(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PR_AF_WORKSPACE_TTL_DAYS", raising=False)
    stray = tmp_path / "notes.txt"
    stray.write_text("keep me")
    _backdate(stray, WEEK)

    _reap_stale_workspaces(str(tmp_path))

    assert stray.exists()
