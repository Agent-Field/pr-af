#!/usr/bin/env python3
"""Replay the merge-gate prompt against findings stored in a benchmark JSON.

Why: iterating the merge-gate prompt against the full pipeline is slow
(spin up server, run multi-phase review, wait for findings). The benchmark
JSON already contains the findings we want to classify. This script loads
them, fires the gate prompt against deepseek/deepseek-v4-pro via OpenRouter
in parallel, and prints a classification report.

Usage:
  python scripts/replay_merge_gate.py benchmark/agentfield-254/pr-af-result-sonnet-254.json
  python scripts/replay_merge_gate.py --model deepseek/deepseek-v4-pro <path>

Env: OPENROUTER_API_KEY must be set.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
from pathlib import Path

# Make `pr_af` importable when this script runs from repo root.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

import httpx  # noqa: E402

from pr_af.merge_gate import _MERGE_GATE_SYSTEM, _parse_verdict  # noqa: E402
from pr_af.schemas.output import ScoredFinding  # noqa: E402


OR_URL = "https://openrouter.ai/api/v1/chat/completions"
DEFAULT_MODEL = "deepseek/deepseek-v4-pro"


def _load_findings(path: str) -> list[ScoredFinding]:
    with open(path) as f:
        blob = json.load(f)
    # Benchmark JSONs come in three shapes:
    #   direct result, async-execution wrapper {result: ...}, or {output_data: ...}.
    if "output_data" in blob:
        result = blob["output_data"]
        if isinstance(result, str):
            result = json.loads(result)
    else:
        result = blob.get("result", blob)
    raw = result.get("findings", [])
    out: list[ScoredFinding] = []
    for r in raw:
        try:
            out.append(ScoredFinding(**r))
        except Exception as exc:  # noqa: BLE001
            print(f"  ! skip malformed finding: {exc}", file=sys.stderr)
    return out


def _user_prompt(f: ScoredFinding) -> str:
    parts = [
        "# Finding\n",
        f"Severity (reviewer's label): {f.severity}\n",
        f"Confidence: {f.confidence:.2f}\n",
        f"File: {f.file_path}:{f.line_start}\n",
        f"Title: {f.title}\n",
        f"\n## Body\n{f.body}\n",
    ]
    if f.evidence:
        parts.append(f"\n## Evidence\n{f.evidence}\n")
    if f.suggestion:
        parts.append(f"\n## Suggested fix\n{f.suggestion}\n")
    parts.append(
        "\n# Question\n"
        "Must this be fixed before this PR is merged to production? "
        "Reply with JSON only."
    )
    return "".join(parts)


async def _classify(
    client: httpx.AsyncClient,
    model: str,
    api_key: str,
    finding: ScoredFinding,
    *,
    max_tokens: int,
) -> tuple[ScoredFinding, bool, str, dict]:
    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": _MERGE_GATE_SYSTEM},
            {"role": "user", "content": _user_prompt(finding)},
        ],
        "max_tokens": max_tokens,
        "temperature": 0.2,
        # Reasoning models need a budget; cap effort to keep cost down.
        "reasoning": {"effort": "low"},
        # Force JSON-object output so reasoning chains don't truncate before the answer.
        "response_format": {"type": "json_object"},
    }
    try:
        resp = await client.post(
            OR_URL,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            json=body,
            timeout=120.0,
        )
        resp.raise_for_status()
        data = resp.json()
    except Exception as exc:  # noqa: BLE001
        return finding, False, f"http error: {exc.__class__.__name__}", {}

    choice = (data.get("choices") or [{}])[0]
    msg = choice.get("message", {})
    content = msg.get("content") or msg.get("reasoning") or ""
    verdict = _parse_verdict(content)
    if verdict.reason in ("gate parse error", "gate non-object"):
        # Stash raw text so the caller can debug.
        verdict.reason = f"PARSE_ERR: {content[:500]}"
    return finding, verdict.blocking, verdict.reason, data.get("usage", {})


async def _run(
    path: str, model: str, max_tokens: int, concurrency: int
) -> None:
    api_key = os.environ.get("OPENROUTER_API_KEY")
    if not api_key:
        print("OPENROUTER_API_KEY not set", file=sys.stderr)
        sys.exit(2)

    findings = _load_findings(path)
    print(f"Loaded {len(findings)} findings from {path}")
    print(f"Model: {model}  max_tokens: {max_tokens}  concurrency: {concurrency}\n")

    sem = asyncio.Semaphore(concurrency)

    async with httpx.AsyncClient() as client:
        async def _bounded(f: ScoredFinding):
            async with sem:
                return await _classify(client, model, api_key, f, max_tokens=max_tokens)

        results = await asyncio.gather(*(_bounded(f) for f in findings))

    blocking = [r for r in results if r[1]]
    advisory = [r for r in results if not r[1]]
    by_orig_sev: dict[str, dict[str, int]] = {}
    total_cost = 0.0
    for f, is_blocking, _, usage in results:
        sev = f.severity
        by_orig_sev.setdefault(sev, {"blocking": 0, "advisory": 0})
        by_orig_sev[sev]["blocking" if is_blocking else "advisory"] += 1
        cost = float(((usage or {}).get("cost_details") or {}).get("upstream_inference_cost", 0.0))
        total_cost += cost

    print("=" * 70)
    print(f"  Blocking (would REQUEST_CHANGES): {len(blocking)}")
    print(f"  Advisory (non-blocking comment):  {len(advisory)}")
    print(f"  Approx upstream cost:              ${total_cost:.4f}")
    print("=" * 70)
    print()

    print("Severity → blocking/advisory split:")
    for sev in ("critical", "important", "suggestion", "nitpick"):
        if sev in by_orig_sev:
            row = by_orig_sev[sev]
            print(f"  {sev:12s} → {row['blocking']:2d} blocking · {row['advisory']:2d} advisory")
    print()

    print("BLOCKING findings (would block merge):")
    print("-" * 70)
    for f, _, reason, _ in blocking:
        print(f"  [{f.severity}] {f.title}")
        print(f"      → {reason}")
        print()

    print("ADVISORY findings (currently labelled critical but reclassified):")
    print("-" * 70)
    reclassified = [r for r in advisory if r[0].severity == "critical"]
    for f, _, reason, _ in reclassified:
        print(f"  [{f.severity}] {f.title}")
        print(f"      → {reason}")
        print()


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("path", help="benchmark JSON containing findings")
    p.add_argument("--model", default=DEFAULT_MODEL)
    p.add_argument("--max-tokens", type=int, default=400)
    p.add_argument("--concurrency", type=int, default=8)
    args = p.parse_args()
    asyncio.run(_run(args.path, args.model, args.max_tokens, args.concurrency))


if __name__ == "__main__":
    main()
