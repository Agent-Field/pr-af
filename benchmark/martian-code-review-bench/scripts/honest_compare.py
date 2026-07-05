#!/usr/bin/env python3
"""Adjusted comparison that credits real non-golden bugs uniformly."""

from __future__ import annotations

import asyncio
import glob
import json
import os
from pathlib import Path
from typing import Any

import httpx

OPENROUTER_API_KEY = os.environ["OPENROUTER_API_KEY"]
MODEL = "anthropic/claude-sonnet-4.6"
JUDGE_FILE = Path(os.environ["CRBENCH_JUDGE_FILE"])
RESULTS_DIR = Path("benchmark/martian-code-review-bench/results")

CLASSIFY_PROMPT = (
    "You are a STRICT staff engineer auditing code-review comments that did NOT match a PR's "
    "human golden comments, to build an adjusted benchmark. Classify each comment from its text: "
    "'real' = describes a genuine, concrete, plausible code defect a senior would want fixed; "
    "'nit' = valid but trivial (style/naming/doc/test-coverage); 'fp' = wrong, vague, speculative, "
    "or not a real problem. Be conservative — when in doubt, 'fp'. Judge ALL players by the same bar. "
    'STRICT JSON: {"c":[{"i":<int>,"k":"real|nit|fp"}]}'
)
MATCH_PROMPT = (
    "Match a reviewer's findings to a PR's GOLDEN comments (same location+root issue, ignore severity). "
    'STRICT JSON: {"goldens":[{"i":<int>,"hit":<bool>}],'
    '"findings":[{"i":<int>,"matches_golden":<bool>}]}'
)


def load_json(path: Path) -> Any:
    with path.open() as handle:
        return json.load(handle)


async def classify(client: httpx.AsyncClient, comments: list[str]) -> list[str]:
    if not comments:
        return []
    labels: list[str] = []
    for chunk_start in range(0, len(comments), 40):
        chunk = comments[chunk_start : chunk_start + 40]
        body = "\n".join(f"[{index}] {comment[:300]}" for index, comment in enumerate(chunk))
        response = await client.post(
            "https://openrouter.ai/api/v1/chat/completions",
            headers={"Authorization": f"Bearer {OPENROUTER_API_KEY}"},
            json={
                "model": MODEL,
                "temperature": 0,
                "response_format": {"type": "json_object"},
                "messages": [
                    {"role": "system", "content": CLASSIFY_PROMPT},
                    {"role": "user", "content": f"## COMMENTS ({len(chunk)})\n{body}"},
                ],
            },
            timeout=150,
        )
        text = response.json()["choices"][0]["message"]["content"]
        start, end = text.find("{"), text.rfind("}")
        try:
            labels.extend(item.get("k") for item in json.loads(text[start : end + 1]).get("c", []))
        except Exception:
            labels.extend(["fp"] * len(chunk))
    return labels


async def tool_metrics(
    client: httpx.AsyncClient,
    tool: str,
    urls: set[str],
    judge_doc: dict[str, Any],
) -> tuple[str, float, float, float, int, int, int, int]:
    true_positives = false_negatives = 0
    false_positive_texts: list[str] = []
    for url in urls:
        entry = judge_doc.get(url, {}).get(tool)
        if not entry:
            continue
        true_positives += entry.get("tp", 0)
        false_negatives += entry.get("fn", 0)
        for item in entry.get("false_positives", []):
            comment = item.get("candidate") or item.get("comment") or ""
            if comment:
                false_positive_texts.append(comment)
    labels = await classify(client, false_positive_texts)
    real = labels.count("real")
    false_positives = labels.count("fp")
    recall = true_positives / (true_positives + false_negatives) if true_positives + false_negatives else 0
    precision = (
        (true_positives + real) / (true_positives + real + false_positives)
        if true_positives + real + false_positives
        else 0
    )
    f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0
    return tool, precision, recall, f1, true_positives, real, false_positives, labels.count("nit")


async def pr_af_metrics(client: httpx.AsyncClient) -> tuple[str, float, float, float, int, int, int, int]:
    result_files = sorted(RESULTS_DIR.glob("*.json"))
    golden_hits = golden_misses = 0
    nongolden_findings: list[str] = []
    semaphore = asyncio.Semaphore(6)

    async def score_one(path: Path) -> None:
        nonlocal golden_hits, golden_misses
        result_doc = load_json(path)
        goldens = result_doc["goldens"]
        findings = result_doc["findings"][:25]
        if not goldens:
            return
        golden_text = "\n".join(f"[G{i}] {item.get('comment')}" for i, item in enumerate(goldens))
        finding_text = "\n".join(
            f"[F{i}] {item.get('title')} :: {(item.get('body') or '')[:200]}"
            for i, item in enumerate(findings)
        )
        async with semaphore:
            response = await client.post(
                "https://openrouter.ai/api/v1/chat/completions",
                headers={"Authorization": f"Bearer {OPENROUTER_API_KEY}"},
                json={
                    "model": MODEL,
                    "temperature": 0,
                    "response_format": {"type": "json_object"},
                    "messages": [
                        {"role": "system", "content": MATCH_PROMPT},
                        {"role": "user", "content": f"## GOLDENS\n{golden_text}\n## FINDINGS\n{finding_text}"},
                    ],
                },
                timeout=150,
            )
        text = response.json()["choices"][0]["message"]["content"]
        start, end = text.find("{"), text.rfind("}")
        verdict = json.loads(text[start : end + 1])
        matched_goldens = sum(1 for item in verdict.get("goldens", []) if item.get("hit"))
        golden_hits += matched_goldens
        golden_misses += len(goldens) - matched_goldens
        matched_findings = {
            item["i"]
            for item in verdict.get("findings", [])
            if item.get("matches_golden") and "i" in item
        }
        for index, finding in enumerate(findings):
            if index not in matched_findings:
                nongolden_findings.append(f"{finding.get('title')} :: {(finding.get('body') or '')[:240]}")

    await asyncio.gather(*(score_one(path) for path in result_files))
    labels = await classify(client, nongolden_findings)
    real = labels.count("real")
    false_positives = labels.count("fp")
    recall = golden_hits / (golden_hits + golden_misses) if golden_hits + golden_misses else 0
    precision = (
        (golden_hits + real) / (golden_hits + real + false_positives)
        if golden_hits + real + false_positives
        else 0
    )
    f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0
    return "GLM-5.2 + PR-AF", precision, recall, f1, golden_hits, real, false_positives, labels.count("nit")


async def main() -> None:
    urls = {load_json(Path(path))["pr_url"] for path in glob.glob(str(RESULTS_DIR / "*.json"))}
    judge_doc = load_json(JUDGE_FILE)
    async with httpx.AsyncClient() as client:
        rows = await asyncio.gather(
            pr_af_metrics(client),
            tool_metrics(client, "cubic-v2", urls, judge_doc),
            tool_metrics(client, "cubic-dev", urls, judge_doc),
        )
    print("\n=== Adjusted metrics: real non-golden bugs credited, nits excluded ===")
    print(f"{'player':22s} {'precision':>9} {'recall':>7} {'F1':>6}   (golden/real/fp/nit)")
    for name, precision, recall, f1, golden, real, false_positive, nit in sorted(rows, key=lambda item: -item[3]):
        print(f"{name:22s} {precision:9.3f} {recall:7.3f} {f1:6.3f}   ({golden}/{real}/{false_positive}/{nit})")


if __name__ == "__main__":
    asyncio.run(main())
