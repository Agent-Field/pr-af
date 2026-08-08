from __future__ import annotations

import importlib
import json
from typing import Any
from unittest.mock import AsyncMock

import httpx
import pytest
from fastapi import FastAPI

app_module = importlib.import_module("pr_af.app")


class _FakeResponse:
    def raise_for_status(self) -> None:
        pass

    def json(self) -> dict[str, str]:
        return {"execution_id": "exec_test"}


class _RecordingAsyncClient:
    def __init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    async def __aenter__(self) -> _RecordingAsyncClient:
        return self

    async def __aexit__(self, *args: object) -> None:
        pass

    async def post(self, url: str, **kwargs: Any) -> _FakeResponse:
        self.calls.append({"url": url, **kwargs})
        return _FakeResponse()


def _webhook_app() -> FastAPI:
    webhook_app = FastAPI()
    webhook_app.add_api_route("/webhook/github", app_module.webhook_github, methods=["POST"])
    return webhook_app


@pytest.fixture(autouse=True)
def clean_webhook_env(monkeypatch: pytest.MonkeyPatch) -> None:
    for name in (
        "PR_AF_MAX_CONCURRENT_REVIEWERS",
        "PR_AF_MAX_REVIEW_DEPTH",
        "PR_AF_MAX_COVERAGE_ITERATIONS",
    ):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(app_module, "_WEBHOOK_SECRET", "")
    monkeypatch.setattr(app_module, "_LABEL_TRIGGER", "pr-af")
    with app_module._webhook_dedupe_lock:
        app_module._seen_deliveries.clear()
        app_module._recent_label_fires.clear()


def _label_payload(label: str, *, action: str = "labeled") -> dict[str, Any]:
    return {
        "action": action,
        "label": {"name": label},
        "pull_request": {
            "number": 42,
            "html_url": "https://github.com/octo/repo/pull/42",
        },
        "repository": {"full_name": "octo/repo"},
    }


@pytest.mark.asyncio
@pytest.mark.parametrize("api_key", ["cp-secret", ""])
async def test_fire_review_uses_node_id_and_optional_auth(
    monkeypatch: pytest.MonkeyPatch, api_key: str
) -> None:
    client = _RecordingAsyncClient()
    monkeypatch.setattr(app_module, "NODE_ID", "custom-node")
    monkeypatch.setattr(app_module, "_CP_URL", "http://control-plane")
    monkeypatch.setattr(app_module, "_CP_API_KEY", api_key)
    monkeypatch.setattr(app_module.httpx, "AsyncClient", lambda **_kwargs: client)

    exec_id = await app_module._fire_review("https://github.com/octo/repo/pull/42")

    assert exec_id == "exec_test"
    assert client.calls[0]["url"] == "http://control-plane/api/v1/execute/async/custom-node.review"
    headers = client.calls[0]["headers"]
    if api_key:
        assert headers.get("X-API-Key") == api_key
    else:
        assert "X-API-Key" not in headers


@pytest.mark.asyncio
async def test_label_trigger_dispatches_and_wrong_label_is_ignored(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fire_review = AsyncMock(return_value="exec_label")
    monkeypatch.setattr(app_module, "_fire_review", fire_review)
    transport = httpx.ASGITransport(app=_webhook_app())

    async with httpx.AsyncClient(transport=transport, base_url="http://test") as webhook_client:
        wrong = await webhook_client.post(
            "/webhook/github",
            json=_label_payload("other"),
            headers={"X-GitHub-Event": "pull_request"},
        )
        assert wrong.status_code == 200
        assert wrong.json() == {"status": "ignored", "reason": "label not a trigger"}
        fire_review.assert_not_awaited()

        triggered = await webhook_client.post(
            "/webhook/github",
            json=_label_payload("pr-af"),
            headers={"X-GitHub-Event": "pull_request"},
        )
        assert triggered.status_code == 200
        assert triggered.json()["status"] == "review_dispatched"
        fire_review.assert_awaited_once_with("https://github.com/octo/repo/pull/42")


@pytest.mark.asyncio
async def test_webhook_caps_use_input_keys_and_ignore_invalid_values(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    client = _RecordingAsyncClient()
    webhook_transport = httpx.ASGITransport(app=_webhook_app())
    webhook_client = httpx.AsyncClient(transport=webhook_transport, base_url="http://test")
    monkeypatch.setattr(app_module.httpx, "AsyncClient", lambda **_kwargs: client)
    monkeypatch.setenv("PR_AF_MAX_CONCURRENT_REVIEWERS", "0")
    monkeypatch.setenv("PR_AF_MAX_REVIEW_DEPTH", "-1")
    monkeypatch.setenv("PR_AF_MAX_COVERAGE_ITERATIONS", "many")

    async with webhook_client:
        response = await webhook_client.post(
            "/webhook/github",
            json=_label_payload("pr-af"),
            headers={"X-GitHub-Event": "pull_request"},
        )
    assert response.status_code == 200
    assert response.json()["status"] == "review_dispatched"
    invalid_input = json.loads(client.calls[-1]["content"])["input"]
    assert "max_concurrent_reviewers" not in invalid_input
    assert "max_review_depth" not in invalid_input
    assert "max_coverage_iterations" not in invalid_input

    monkeypatch.setenv("PR_AF_MAX_CONCURRENT_REVIEWERS", "1")
    monkeypatch.setenv("PR_AF_MAX_REVIEW_DEPTH", "0")
    monkeypatch.setenv("PR_AF_MAX_COVERAGE_ITERATIONS", "2")
    assert await app_module._fire_review("https://github.com/octo/repo/pull/42") == "exec_test"
    valid_input = json.loads(client.calls[-1]["content"])["input"]
    assert valid_input["max_concurrent_reviewers"] == 1
    assert valid_input["max_review_depth"] == 0
    assert valid_input["max_coverage_iterations"] == 2


@pytest.mark.asyncio
async def test_label_trigger_dedupes_delivery_and_recent_pr(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fire_review = AsyncMock(return_value="exec_label")
    monkeypatch.setattr(app_module, "_fire_review", fire_review)
    now = 1000.0
    monkeypatch.setattr(app_module.time, "monotonic", lambda: now)
    transport = httpx.ASGITransport(app=_webhook_app())
    payload = _label_payload("pr-af")

    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        first = await client.post(
            "/webhook/github",
            json=payload,
            headers={"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "delivery-1"},
        )
        duplicate = await client.post(
            "/webhook/github",
            json=payload,
            headers={"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "delivery-1"},
        )
        recent = await client.post(
            "/webhook/github",
            json=payload,
            headers={"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "delivery-2"},
        )
        now += app_module._LABEL_FIRE_TTL_SECONDS + 1
        after_ttl = await client.post(
            "/webhook/github",
            json=payload,
            headers={"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "delivery-3"},
        )

    assert first.json()["status"] == "review_dispatched"
    assert duplicate.json() == {"status": "ignored", "reason": "duplicate delivery"}
    assert recent.json() == {"status": "ignored", "reason": "recently dispatched"}
    assert after_ttl.json()["status"] == "review_dispatched"
    assert fire_review.await_count == 2
