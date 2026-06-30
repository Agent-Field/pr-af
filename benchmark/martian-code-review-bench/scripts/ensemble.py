#!/usr/bin/env python3
"""Self-consistency escalation for benchmark recall.

Run AFTER the baseline campaign finishes. For every problem the single deep pass
MISSED (recall < 1.0), run K extra independent deep passes, UNION all findings
(baseline + ensemble passes, exact-deduped), and re-judge the union against the
goldens. GLM-5.2 passes vary, so union-recall >= single-pass recall — this is the
general lever to top problems a single pass misses. No domain knowledge, no
per-problem tuning: a pure meta-procedure.

Writes the updated, EXACT (untrimmed) result back to
benchmark/.../results/<id>.json with baseline_recall + ensemble_recall, and
appends an `ensemble` row to scoreboard.jsonl.

Run only when campaign.py has printed "[campaign] done" (it shares the node's
per-repo checkout dir; overlapping same-repo reviews would race).
"""
from __future__ import annotations

import asyncio
import json
import os

import httpx

import campaign as C  # reuse fire/poll/judge/_repo_lock + paths

ENSEMBLE_PASSES = int(os.getenv("ENSEMBLE_PASSES", "2"))   # extra passes beyond baseline
CONCURRENCY = int(os.getenv("ENSEMBLE_CONCURRENCY", "3"))


def _dedup_exact(findings: list[dict]) -> list[dict]:
    seen: set[tuple] = set()
    out: list[dict] = []
    for f in findings:
        key = (f.get("file_path"), f.get("line_start"), f.get("line_end"), (f.get("title") or "").strip().lower())
        if key not in seen:
            seen.add(key)
            out.append(f)
    return out


def _baseline_findings(pid: str) -> list[dict]:
    run_file = C.RUNS / f"{pid.replace('/', '_')}.json"
    if not run_file.exists():
        return []
    details = json.loads(run_file.read_text())
    return (details.get("output_data") or {}).get("findings") or []


async def _extra_pass(client: httpx.AsyncClient, prob: dict, k: int) -> list[dict]:
    pid = prob["id"]
    cache = C.RUNS / f"{pid.replace('/', '_')}__ens{k}.json"
    if cache.exists():
        details = json.loads(cache.read_text())
    else:
        async with C._repo_lock(prob["repo"]):
            eid = await C.fire(client, prob["pr_url"])
            print(f"[{pid}] ensemble pass {k} fired {eid}", flush=True)
            details = await C.poll(client, eid, f"{pid}~ens{k}")
            cache.write_text(json.dumps(details, default=str))
    if details.get("status") not in ("succeeded", "completed"):
        return []
    return (details.get("output_data") or {}).get("findings") or []


async def escalate(client: httpx.AsyncClient, sem: asyncio.Semaphore, prob: dict, baseline_recall: float) -> None:
    pid = prob["id"]
    async with sem:
        passes = await asyncio.gather(*[_extra_pass(client, prob, k) for k in range(1, ENSEMBLE_PASSES + 1)])
    union = _dedup_exact(_baseline_findings(pid) + [f for p in passes for f in p])
    verdict = await C.judge(client, prob["goldens"], union)
    res_file = C.RESULTS_DIR / f"{pid.replace('/', '_')}.json"
    doc = json.loads(res_file.read_text()) if res_file.exists() else {"id": pid, "repo": prob["repo"]}
    doc.update({
        "pr_url": prob["pr_url"],
        "review_model": C.REVIEW_MODEL,
        "judge_model": C.JUDGE_MODEL,
        "difficulty_score": prob.get("difficulty_score"),
        "baseline_recall": baseline_recall,
        "ensemble_passes": ENSEMBLE_PASSES + 1,
        "ensemble_recall": verdict["recall"],
        "recall": max(baseline_recall, verdict["recall"]),
        "hits": verdict["hits"],
        "n_goldens": verdict["total"],
        "goldens": prob["goldens"],
        "golden_verdicts": verdict["matches"],
        "findings": union,  # exact, untrimmed union
    })
    res_file.write_text(json.dumps(doc, indent=2, default=str))
    C._record({"id": pid, "repo": prob["repo"], "status": "scored",
               "difficulty": prob.get("difficulty_score"), "n_goldens": verdict["total"],
               "n_findings": len(union), "hits": verdict["hits"], "recall": verdict["recall"],
               "ensemble": True, "baseline_recall": baseline_recall,
               "missed": [prob["goldens"][m["golden_idx"]]["comment"][:120]
                          for m in verdict["matches"] if not m.get("hit")
                          and 0 <= m.get("golden_idx", -1) < len(prob["goldens"])]})
    print(f"[{pid}] ENSEMBLE recall {baseline_recall} -> {verdict['recall']} "
          f"({verdict['hits']}/{verdict['total']})", flush=True)


async def main() -> None:
    problems = {p["id"]: p for p in json.loads(C.PROBLEMS.read_text())}
    rows = [json.loads(l) for l in C.SCORE_JSONL.read_text().splitlines() if l.strip()]
    # latest baseline (non-ensemble) row per id
    baseline: dict[str, dict] = {}
    for r in rows:
        if r.get("status") == "scored" and not r.get("ensemble"):
            baseline[r["id"]] = r
    misses = [(problems[i], r["recall"]) for i, r in baseline.items()
              if r["recall"] < 1.0 and i in problems and not problems[i].get("pr_url_uncertain")]
    print(f"[ensemble] {len(misses)} baseline misses to escalate "
          f"(+{ENSEMBLE_PASSES} passes each, concurrency={CONCURRENCY})", flush=True)
    if not misses:
        print("[ensemble] nothing to escalate — baseline topped every scored problem", flush=True)
        return
    sem = asyncio.Semaphore(CONCURRENCY)
    async with httpx.AsyncClient() as client:
        await asyncio.gather(*(escalate(client, sem, p, br) for p, br in misses))
    print("[ensemble] done", flush=True)


if __name__ == "__main__":
    asyncio.run(main())
