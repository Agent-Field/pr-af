from __future__ import annotations

from pr_af.config import AIIntegrationConfig


def test_aforge_provider_and_binary_overrides(monkeypatch) -> None:
    monkeypatch.setenv("PR_AF_PROVIDER", "aforge")
    monkeypatch.setenv("PR_AF_AFORGE_BIN", "/opt/aforge")

    config = AIIntegrationConfig.from_env()

    assert config.provider == "aforge"
    assert config.aforge_bin == "/opt/aforge"
    assert config.harness_bin == ""


def test_generic_harness_binary_is_available_to_all_providers(monkeypatch) -> None:
    monkeypatch.setenv("PR_AF_HARNESS_BIN", "/opt/harness")

    config = AIIntegrationConfig.from_env()

    assert config.harness_bin == "/opt/harness"


def test_aforge_exec_is_the_default(monkeypatch) -> None:
    monkeypatch.delenv("PR_AF_PROVIDER", raising=False)
    monkeypatch.delenv("AGENTFIELD_AFORGE_COMMAND", raising=False)

    config = AIIntegrationConfig.from_env()

    assert config.provider == "aforge"
    assert config.provider_env()["AGENTFIELD_AFORGE_COMMAND"] == "exec"


def test_opencode_remains_an_explicit_rollback(monkeypatch) -> None:
    monkeypatch.setenv("PR_AF_PROVIDER", "opencode")

    assert AIIntegrationConfig.from_env().provider == "opencode"
