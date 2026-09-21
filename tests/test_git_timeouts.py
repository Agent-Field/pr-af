"""Contract tests for configurable Git timeouts and LFS smudge behavior."""

from __future__ import annotations

import asyncio
import os
import shlex
import subprocess
import threading
import time
from pathlib import Path

import pytest
import yaml

import pr_af.app as app_module
import pr_af.github.client as github_client_module
from pr_af.app import _checkout_pr_branch, _resolve_repo
from pr_af.gitconfig import (
    GIT_TIMEOUT_DEFAULTS,
    GIT_TIMEOUT_ENV,
    GIT_TIMEOUT_ENV_VARS,
    git_timeout_seconds,
)
from pr_af.github.client import GitHubClient
from pr_af.orchestrator import ReviewOrchestrator
from pr_af.schemas.input import ReviewInput

_TIMEOUT_ENVS = [GIT_TIMEOUT_ENV, *GIT_TIMEOUT_ENV_VARS.values()]


@pytest.fixture(autouse=True)
def _clear_git_timeout_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    for env_name in _TIMEOUT_ENVS:
        monkeypatch.delenv(env_name, raising=False)


def _git(*args: str, cwd: Path) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, check=True, capture_output=True, text=True
    ).stdout.strip()


def _make_filter_upstream(path: Path) -> None:
    path.mkdir()
    _git("init", "-q", "-b", "main", cwd=path)
    _git("config", "user.email", "test@example.com", cwd=path)
    _git("config", "user.name", "test", cwd=path)
    _git("config", "commit.gpgsign", "false", cwd=path)
    (path / ".gitattributes").write_text("*.bin filter=controlled\n")
    (path / "asset.bin").write_text("main\n")
    _git("add", "-A", cwd=path)
    _git("commit", "-qm", "initial", cwd=path)

    _git("checkout", "-q", "-b", "_pr1", "main", cwd=path)
    (path / "asset.bin").write_text("pr\n")
    _git("commit", "-qam", "pr1", cwd=path)
    sha = _git("rev-parse", "HEAD", cwd=path)
    _git("update-ref", "refs/pull/1/head", sha, cwd=path)
    _git("checkout", "-q", "main", cwd=path)
    _git("branch", "-qD", "_pr1", cwd=path)


def _clone_without_checkout(upstream: Path, target: Path) -> None:
    _git(
        "clone",
        "--depth",
        "1",
        "--no-tags",
        "--no-checkout",
        str(upstream),
        str(target),
        cwd=upstream.parent,
    )


def _install_sleeping_git(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, operation: str
) -> None:
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    fake_git = bin_dir / "git"
    fake_git.write_text(
        "#!/bin/sh\n"
        'for arg in "$@"; do\n'
        f'  [ "$arg" = "{operation}" ] && exec sleep 10\n'
        "done\n"
        "exit 0\n"
    )
    fake_git.chmod(0o755)
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")


def test_python_timeout_names_and_defaults_match_contract() -> None:
    expected_envs = {
        "clone": "PR_AF_GIT_CLONE_TIMEOUT_SECONDS",
        "fetch": "PR_AF_GIT_FETCH_TIMEOUT_SECONDS",
        "checkout": "PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS",
        "diff": "PR_AF_GIT_DIFF_TIMEOUT_SECONDS",
    }
    expected_defaults = {
        "clone": 600.0,
        "fetch": 600.0,
        "checkout": 600.0,
        "diff": None,
    }

    assert GIT_TIMEOUT_ENV == "PR_AF_GIT_TIMEOUT_SECONDS"
    assert expected_envs == GIT_TIMEOUT_ENV_VARS
    assert expected_defaults == GIT_TIMEOUT_DEFAULTS
    assert {
        operation: git_timeout_seconds(operation) for operation in expected_defaults
    } == expected_defaults


def test_umbrella_timeout_and_per_operation_precedence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("PR_AF_GIT_TIMEOUT_SECONDS", "17.5")
    assert {operation: git_timeout_seconds(operation) for operation in GIT_TIMEOUT_DEFAULTS} == {
        operation: 17.5 for operation in GIT_TIMEOUT_DEFAULTS
    }

    monkeypatch.setenv("PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", "2.25")
    assert git_timeout_seconds("checkout") == 2.25
    assert git_timeout_seconds("clone") == 17.5
    assert git_timeout_seconds("fetch") == 17.5
    assert git_timeout_seconds("diff") == 17.5


@pytest.mark.parametrize("raw", ["abc", "-5", "0"])
def test_invalid_umbrella_timeout_falls_back_and_logs(
    raw: str,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("PR_AF_GIT_TIMEOUT_SECONDS", raw)

    assert git_timeout_seconds("checkout") == 600.0
    assert capsys.readouterr().out == (
        f"[PR-AF] Ignoring invalid PR_AF_GIT_TIMEOUT_SECONDS={raw}; using 600s\n"
    )


def test_invalid_operation_timeout_uses_builtin_not_umbrella(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("PR_AF_GIT_TIMEOUT_SECONDS", "9")
    monkeypatch.setenv("PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", "invalid")

    assert git_timeout_seconds("checkout") == 600.0
    assert git_timeout_seconds("fetch") == 9.0


def test_empty_timeout_is_unset_without_invalid_value_log(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("PR_AF_GIT_TIMEOUT_SECONDS", "")

    assert git_timeout_seconds("checkout") == 600.0
    assert capsys.readouterr().out == ""


def test_empty_operation_timeout_falls_through_to_umbrella(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("PR_AF_GIT_TIMEOUT_SECONDS", "9")
    monkeypatch.setenv("PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", "")

    assert git_timeout_seconds("checkout") == 9.0
    assert capsys.readouterr().out == ""


def test_checkout_timeout_kills_filter_process_group(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    upstream = tmp_path / "upstream"
    _make_filter_upstream(upstream)
    target = tmp_path / "workspace"
    _clone_without_checkout(upstream, target)
    _git("checkout", "-q", "main", cwd=target)
    pid_file = tmp_path / "slow-filter.pid"
    filter_command = (
        "sh -c 'echo $$ > \"$1\"; sleep 30; cat' sh "
        f"{shlex.quote(str(pid_file))}"
    )
    _git("config", "filter.controlled.smudge", filter_command, cwd=target)
    monkeypatch.setenv("PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", "2")

    started = time.monotonic()
    with pytest.raises(subprocess.TimeoutExpired) as raised:
        _checkout_pr_branch(str(target), 1)
    elapsed = time.monotonic() - started

    assert raised.value.timeout == 2.0
    assert elapsed < 8
    assert not (target / ".git" / "index.lock").exists()
    assert not (target / ".git" / "shallow.lock").exists()

    _git("config", "filter.controlled.smudge", "cat", cwd=target)
    monkeypatch.setenv("PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS", "10")
    _checkout_pr_branch(str(target), 1)
    assert (target / "asset.bin").read_text() == "pr\n"

    try:
        filter_pid = int(pid_file.read_text().strip())
    except FileNotFoundError:
        pytest.skip("slow smudge filter did not record its PID")

    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        try:
            os.kill(filter_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.05)
    else:
        pytest.fail(f"slow smudge filter process {filter_pid} survived git timeout")


@pytest.mark.parametrize(
    "operator_value, expected",
    [(None, "SKIP=1\n"), ("", "SKIP=1\n"), ("0", "SKIP=0\n")],
)
def test_checkout_smudge_receives_lfs_environment(
    operator_value: str | None,
    expected: str,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    upstream = tmp_path / "upstream"
    _make_filter_upstream(upstream)
    target = tmp_path / "workspace"
    _clone_without_checkout(upstream, target)
    _git(
        "config",
        "filter.controlled.smudge",
        "printf 'SKIP=%s\\n' \"$GIT_LFS_SKIP_SMUDGE\"",
        cwd=target,
    )
    if operator_value is None:
        monkeypatch.delenv("GIT_LFS_SKIP_SMUDGE", raising=False)
    else:
        monkeypatch.setenv("GIT_LFS_SKIP_SMUDGE", operator_value)

    _checkout_pr_branch(str(target), 1)

    assert (target / "asset.bin").read_text() == expected


def test_resolve_repo_clone_uses_resolved_timeout(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _install_sleeping_git(tmp_path, monkeypatch, "clone")
    monkeypatch.setenv("PR_AF_WORKDIR", str(tmp_path / "workspaces"))
    monkeypatch.setenv("PR_AF_GIT_CLONE_TIMEOUT_SECONDS", "1")

    with pytest.raises(subprocess.TimeoutExpired) as raised:
        _resolve_repo(None, "https://github.com/owner/repo/pull/1")

    assert raised.value.timeout == 1.0


def test_resolve_repo_reused_workspace_fetch_uses_resolved_timeout(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    workdir = tmp_path / "workspaces"
    (workdir / "repo-pr1" / ".git").mkdir(parents=True)
    _install_sleeping_git(tmp_path, monkeypatch, "fetch")
    monkeypatch.setenv("PR_AF_WORKDIR", str(workdir))
    monkeypatch.setenv("PR_AF_GIT_FETCH_TIMEOUT_SECONDS", "1")

    with pytest.raises(subprocess.TimeoutExpired) as raised:
        _resolve_repo(None, "https://github.com/owner/repo/pull/1")

    assert raised.value.timeout == 1.0


@pytest.mark.asyncio
async def test_github_client_clone_uses_shared_runner_and_resolved_timeout(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    observed: dict[str, object] = {}

    def fake_run_git(
        cmd: list[str], *, timeout: float | None, text: bool = True
    ) -> subprocess.CompletedProcess[str]:
        observed.update(cmd=cmd, timeout=timeout, text=text)
        return subprocess.CompletedProcess(cmd, 0, "", "")

    monkeypatch.setattr(github_client_module, "run_git", fake_run_git)
    monkeypatch.setenv("PR_AF_GIT_CLONE_TIMEOUT_SECONDS", "3.25")

    target = str(tmp_path / "clone")
    client = GitHubClient(token="token")
    client._use_app_auth = False
    assert await client.clone_repo("owner", "repo", target) == target
    assert observed["timeout"] == 3.25
    assert observed["cmd"] == [
        "git",
        "clone",
        "--depth",
        "1",
        "https://x-access-token:token@github.com/owner/repo.git",
        target,
    ]


@pytest.mark.asyncio
async def test_github_client_clone_preserves_called_process_error(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    def failed_run_git(
        cmd: list[str], *, timeout: float | None, text: bool = True
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(cmd, 7, "", "clone failed")

    monkeypatch.setattr(github_client_module, "run_git", failed_run_git)
    client = GitHubClient(token="token")
    client._use_app_auth = False

    with pytest.raises(subprocess.CalledProcessError) as raised:
        await client.clone_repo("owner", "repo", str(tmp_path / "clone"))

    assert raised.value.returncode == 7
    assert raised.value.stderr == "clone failed"


def test_deployment_files_expose_git_configuration_without_shadowing() -> None:
    repo_root = Path(__file__).resolve().parents[1]
    git_variables = {
        GIT_TIMEOUT_ENV,
        *GIT_TIMEOUT_ENV_VARS.values(),
        "GIT_LFS_SKIP_SMUDGE",
    }
    compose_variables = git_variables | {
        "PR_AF_MAX_COST_USD",
        "PR_AF_MAX_DURATION_SECONDS",
    }

    for filename, service in (
        ("docker-compose.yml", "pr-af"),
        ("docker-compose.go.yml", "pr-af-go"),
    ):
        document = yaml.safe_load((repo_root / filename).read_text())
        service_config = document["services"][service]
        assert service_config["init"] is True
        assert compose_variables <= set(service_config["environment"])

    for filename in ("agentfield-package.yaml", "go/agentfield-package.yaml"):
        document = yaml.safe_load((repo_root / filename).read_text())
        optional = {
            entry["name"]: entry for entry in document["user_environment"]["optional"]
        }
        assert git_variables <= optional.keys()
        for variable in GIT_TIMEOUT_ENV_VARS.values():
            assert "default" not in optional[variable]
        assert optional["GIT_LFS_SKIP_SMUDGE"]["default"] == "1"


def test_diff_timeout_surfaces_value_error_naming_configuration(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    fake_git = bin_dir / "git"
    fake_git.write_text("#!/bin/sh\nexec sleep 10\n")
    fake_git.chmod(0o755)
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")
    monkeypatch.setenv("PR_AF_GIT_DIFF_TIMEOUT_SECONDS", "0.1")
    orchestrator = ReviewOrchestrator(app=object(), input=ReviewInput(diff_text="diff"))

    with pytest.raises(ValueError, match="PR_AF_GIT_DIFF_TIMEOUT_SECONDS"):
        orchestrator._compute_repo_diff(str(tmp_path), None, None)


@pytest.mark.asyncio
async def test_review_resolves_repo_off_event_loop(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    event_loop_thread = threading.get_ident()
    resolver_thread: list[int] = []

    def slow_resolve(_repo_path: str | None, _pr_url: str | None) -> str:
        resolver_thread.append(threading.get_ident())
        time.sleep(0.3)
        return str(tmp_path)

    class Result:
        def model_dump(self) -> dict[str, bool]:
            return {"ok": True}

    class Orchestrator:
        def __init__(self, **_kwargs: object) -> None:
            pass

        async def run(self) -> Result:
            return Result()

    monkeypatch.setattr(app_module, "_resolve_repo", slow_resolve)
    monkeypatch.setattr(app_module, "ReviewOrchestrator", Orchestrator)

    loop = asyncio.get_running_loop()
    started = loop.time()
    review_impl = getattr(app_module.review, "_original_func", app_module.review)
    review_task = asyncio.create_task(review_impl(repo_path=str(tmp_path)))
    await asyncio.sleep(0.05)

    assert loop.time() - started < 0.2
    assert not review_task.done()
    assert resolver_thread and resolver_thread[0] != event_loop_thread
    assert await review_task == {"ok": True}
