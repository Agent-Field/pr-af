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
