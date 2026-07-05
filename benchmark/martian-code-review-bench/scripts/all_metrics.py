#!/usr/bin/env python3
"""Golden-only precision/recall/F1 against the Martian leaderboard tools."""

from __future__ import annotations

import asyncio
import glob
import json
import os
import re
import subprocess
from pathlib import Path
from typing import Any

import httpx

OPENROUTER_API_KEY = os.environ["OPENROUTER_API_KEY"]
MODEL = "anthropic/claude-sonnet-4.6"
RESULTS_DIR = Path("benchmark/martian-code-review-bench/results")
CRBENCH_RESULTS_DIR = Path(os.environ["CRBENCH_RESULTS_DIR"])
OUTPUT_FILE = Path("_glm52_bench/golden_metrics.json")
SYSTEM_PROMPT = (
    "Score a reviewer vs a PR's GOLDEN comments, both directions. Match = same code location "
    "AND same root issue (paraphrases match). IGNORE severity. STRICT JSON: "
    '{"goldens":[{"i":<int>,"hit":<bool>}],"findings":[{"i":<int>,"matches_golden":<bool>}]}'
)


def changed_ranges(owner: str, repo: str, number: str) -> dict[str, list[tuple[int, int]]]:
    result = subprocess.run(
        ["gh", "pr", "diff", number, "--repo", f"{owner}/{repo}"],
        capture_output=True,
        text=True,
        timeout=90,
        check=False,
    )
    ranges_by_file: dict[str, list[tuple[int, int]]] = {}
    current_file: str | None = None
    for line in result.stdout.splitlines():
        if line.startswith("diff --git"):
            match = re.search(r" b/(.+)$", line)
            current_file = match.group(1) if match else None
        elif line.startswith("@@") and current_file:
            match = re.search(r"\+(\d+)(?:,(\d+))?", line)
            if match:
                start = int(match.group(1))
                length = int(match.group(2) or "1")
                ranges_by_file.setdefault(current_file, []).append((start, start + length))
    return ranges_by_file


def finding_in_diff(finding: dict[str, Any], ranges_by_file: dict[str, list[tuple[int, int]]]) -> bool:
    file_path = finding.get("file_path", "")
    matched_key = next(
        (
            key
            for key in ranges_by_file
            if key == file_path
            or key.endswith("/" + file_path)
            or file_path.endswith("/" + key)
            or key.split("/")[-1] == file_path.split("/")[-1]
        ),
        None,
    )
    return bool(matched_key) and any(
        start <= finding.get("line_start", 0) <= end
        for start, end in ranges_by_file[matched_key]
    )


async def judge(
    client: httpx.AsyncClient,
    goldens: list[dict[str, Any]],
    findings: list[dict[str, Any]],
) -> dict[str, Any]:
    golden_text = "\n".join(f"[G{i}] {item.get('comment')}" for i, item in enumerate(goldens))
    finding_text = "\n".join(
        f"[F{i}] {item.get('file_path')}:{item.get('line_start')} "
        f"{item.get('title')} :: {(item.get('body') or '')[:200]}"
        for i, item in enumerate(findings)
    )
    response = await client.post(
        "https://openrouter.ai/api/v1/chat/completions",
        headers={"Authorization": f"Bearer {OPENROUTER_API_KEY}"},
        json={
            "model": MODEL,
            "temperature": 0,
            "response_format": {"type": "json_object"},
            "messages": [
                {"role": "system", "content": SYSTEM_PROMPT},
                {
                    "role": "user",
                    "content": f"## GOLDENS\n{golden_text}\n\n## FINDINGS\n{finding_text or '(none)'}",
                },
            ],
        },
        timeout=150,
    )
    text = response.json()["choices"][0]["message"]["content"]
    start, end = text.find("{"), text.rfind("}")
    return json.loads(text[start : end + 1])


def load_json(path: Path) -> Any:
    with path.open() as handle:
        return json.load(handle)


def rank(scores: dict[str, float], name: str = "GLM-5.2 + PR-AF") -> int:
    return sorted(scores, key=lambda key: -scores[key]).index(name) + 1


async def main() -> None:
    result_files = sorted(RESULTS_DIR.glob("*.json"))
    semaphore = asyncio.Semaphore(6)
    golden_hits = golden_misses = finding_hits = finding_misses = 0

    async def score_one(path: Path) -> None:
        nonlocal golden_hits, golden_misses, finding_hits, finding_misses
        result_doc = load_json(path)
        goldens = result_doc["goldens"]
        if not goldens:
            return
        match = re.search(r"github\.com/([^/]+)/([^/]+)/pull/(\d+)", result_doc["pr_url"])
        try:
            ranges_by_file = changed_ranges(match.group(1), match.group(2), match.group(3)) if match else {}
        except Exception:
            ranges_by_file = {}
        posted = (
            [finding for finding in result_doc["findings"] if finding_in_diff(finding, ranges_by_file)]
            if ranges_by_file
            else result_doc["findings"][:12]
        )
        async with semaphore, httpx.AsyncClient() as client:
            try:
                verdict = await judge(client, goldens, posted)
            except Exception as exc:
                print("err", result_doc["id"], repr(exc)[:40])
                return
        matched_goldens = sum(1 for item in verdict.get("goldens", []) if item.get("hit"))
        matched_findings = sum(1 for item in verdict.get("findings", []) if item.get("matches_golden"))
        judged_findings = len(verdict.get("findings", [])) or len(posted)
        golden_hits += matched_goldens
        golden_misses += len(goldens) - matched_goldens
        finding_hits += matched_findings
        finding_misses += judged_findings - matched_findings

    await asyncio.gather(*(score_one(path) for path in result_files))
    recall = golden_hits / (golden_hits + golden_misses) if golden_hits + golden_misses else 0
    precision = finding_hits / (finding_hits + finding_misses) if finding_hits + finding_misses else 0
    f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0

    urls = {load_json(path)["pr_url"] for path in result_files}
    judge_files = glob.glob(str(CRBENCH_RESULTS_DIR / "*" / "evaluations.json"))
    judge_docs = [load_json(Path(path)) for path in judge_files]
    tool_counts: dict[str, list[int]] = {}
    for judge_doc in judge_docs:
        for url in urls:
            for tool, entry in judge_doc.get(url, {}).items():
                counts = tool_counts.setdefault(tool, [0, 0, 0])
                counts[0] += entry.get("tp", 0)
                counts[1] += entry.get("fp", 0)
                counts[2] += entry.get("fn", 0)

    precision_scores = {
        tool: (tp / (tp + fp) if tp + fp else 0)
        for tool, (tp, fp, _fn) in tool_counts.items()
    }
    recall_scores = {
        tool: (tp / (tp + fn) if tp + fn else 0)
        for tool, (tp, _fp, fn) in tool_counts.items()
    }
    precision_scores["GLM-5.2 + PR-AF"] = precision
    recall_scores["GLM-5.2 + PR-AF"] = recall
    f1_scores = {
        tool: (
            2 * precision_scores[tool] * recall_scores[tool] / (precision_scores[tool] + recall_scores[tool])
            if precision_scores[tool] + recall_scores[tool]
            else 0
        )
        for tool in precision_scores
    }

    n_tools = len(tool_counts) + 1
    print(f"\n=== GLM-5.2 + PR-AF, golden-only, micro over {len(result_files)} PRs ===")
    print(f"recall    = {recall:.3f}  -> #{rank(recall_scores)} of {n_tools}")
    print(f"precision = {precision:.3f}  -> #{rank(precision_scores)} of {n_tools}")
    print(f"F1        = {f1:.3f}  -> #{rank(f1_scores)} of {n_tools}")

    OUTPUT_FILE.parent.mkdir(exist_ok=True)
    with OUTPUT_FILE.open("w") as handle:
        json.dump(
            {
                "recall": recall,
                "precision": precision,
                "f1": f1,
                "ranks": {
                    "recall": rank(recall_scores),
                    "precision": rank(precision_scores),
                    "f1": rank(f1_scores),
                },
            },
            handle,
            indent=2,
        )

    for name, scores in [("F1", f1_scores), ("RECALL", recall_scores), ("PRECISION", precision_scores)]:
        print(f"\n--- {name} top 8 ---")
        for index, (tool, value) in enumerate(sorted(scores.items(), key=lambda item: -item[1])[:8], 1):
            marker = "  <===" if tool == "GLM-5.2 + PR-AF" else ""
            print(f"{index:2d}. {tool:24s} {value:.3f}{marker}")


if __name__ == "__main__":
    asyncio.run(main())
