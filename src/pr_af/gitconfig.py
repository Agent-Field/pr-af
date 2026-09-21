"""Shared environment and timeout configuration for git subprocesses."""

from __future__ import annotations

import math
import os
import signal
import subprocess
from contextlib import suppress

GIT_TIMEOUT_ENV = "PR_AF_GIT_TIMEOUT_SECONDS"
GIT_TIMEOUT_ENV_VARS = {
    "clone": "PR_AF_GIT_CLONE_TIMEOUT_SECONDS",
    "fetch": "PR_AF_GIT_FETCH_TIMEOUT_SECONDS",
    "checkout": "PR_AF_GIT_CHECKOUT_TIMEOUT_SECONDS",
    "diff": "PR_AF_GIT_DIFF_TIMEOUT_SECONDS",
}
GIT_TIMEOUT_DEFAULTS: dict[str, float | None] = {
    "clone": 600.0,
    "fetch": 600.0,
    "checkout": 600.0,
    "diff": None,
}


def _invalid_timeout(env_name: str, raw: str, default: float | None) -> None:
    fallback = "no timeout" if default is None else f"{default:g}s"
    print(
        f"[PR-AF] Ignoring invalid {env_name}={raw}; using {fallback}",
        flush=True,
    )


def git_timeout_seconds(operation: str) -> float | None:
    """Resolve one git operation's timeout from its env and umbrella env."""
    try:
        operation_env = GIT_TIMEOUT_ENV_VARS[operation]
        default = GIT_TIMEOUT_DEFAULTS[operation]
    except KeyError as exc:
        raise ValueError(f"unknown git operation: {operation}") from exc

    for env_name in (operation_env, GIT_TIMEOUT_ENV):
        if env_name not in os.environ:
            continue
        raw = os.environ[env_name]
        if raw == "":
            continue
        try:
            timeout = float(raw)
        except ValueError:
            _invalid_timeout(env_name, raw, default)
            return default
        if not math.isfinite(timeout) or timeout <= 0:
            _invalid_timeout(env_name, raw, default)
            return default
        return timeout
    return default


def git_env() -> dict[str, str]:
    """Return the non-interactive environment shared by every git command."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "echo"}
    # The runtime image installs git without git-lfs, so smudge could not run
    # there anyway. Set this explicitly so behavior is independent of the
    # image/base distro, while preserving an operator-provided value such as 0.
    if not env.get("GIT_LFS_SKIP_SMUDGE"):
        env["GIT_LFS_SKIP_SMUDGE"] = "1"
    return env


def _kill_git_process_group(proc: subprocess.Popen[str]) -> None:
    """Stop git and every helper/filter in the session it created."""
    try:
        if hasattr(os, "killpg"):
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        else:
            proc.kill()
    except (ProcessLookupError, PermissionError):
        # The process may have exited between the timeout and cancellation.
        pass


def run_git(
    cmd: list[str], *, timeout: float | None, text: bool = True
) -> subprocess.CompletedProcess[str]:
    """Run one git command, killing its entire process group on timeout."""
    proc = subprocess.Popen(
        cmd,
        env=git_env(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=text,
        start_new_session=True,
    )
    try:
        stdout, stderr = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        _kill_git_process_group(proc)
        try:
            stdout, stderr = proc.communicate(timeout=1.0)
        except subprocess.TimeoutExpired as drain_error:
            # A process that deliberately escaped the new session can still
            # retain a pipe. Never let that defeat the caller's timeout.
            stdout, stderr = drain_error.output, drain_error.stderr
            for pipe in (proc.stdout, proc.stderr):
                if pipe is not None:
                    pipe.close()
            with suppress(subprocess.TimeoutExpired, ProcessLookupError):
                proc.wait(timeout=0.1)
        raise subprocess.TimeoutExpired(
            cmd,
            timeout,
            output=stdout,
            stderr=stderr,
        ) from None

    return subprocess.CompletedProcess(cmd, proc.returncode, stdout, stderr)
