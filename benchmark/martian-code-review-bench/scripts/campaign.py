#!/usr/bin/env python3
"""Self-driving Martian Code-Review-Bench campaign for GLM-5.2 + PR-AF.

For each runnable problem (hardest-first), fire a blind deep review against the
PR-AF runner, poll to completion, then LLM-judge the findings against the golden
comments (RECALL only — severity labels ignored, per the goal: a HIT = the bug
was FOUND). Writes an incremental scoreboard. Resumable: a problem whose raw run
already exists in runs/<id>.json is re-judged, not re-run.

Concurrency-limited so we do not thrash the local runner / opencode semaphore.
Cost is no object; quality (recall) is the metric.
"""
from __future__ import annotations

import asyncio
import json
import os
import time
from pathlib import Path

import httpx
from dotenv import load_dotenv

# scripts/ -> martian-code-review-bench (BENCH_DIR) -> benchmark -> pr-af (ROOT)
HERE = Path(__file__).resolve().parent
BENCH_DIR = HERE.parent
ROOT = HERE.parents[2]
load_dotenv(ROOT / ".env")

CP = "http://localhost:8080"
OR_KEY = os.environ["OPENROUTER_API_KEY"]
JUDGE_MODEL = "anthropic/claude-sonnet-4.6"
REVIEW_MODEL = "openrouter/z-ai/glm-5.2"

PROBLEMS = BENCH_DIR / "problems.json"
# raw execution details (large) — gitignored scratch under the repo root
RUNS = ROOT / "_glm52_bench" / "runs"
RUNS.mkdir(parents=True, exist_ok=True)

# Curated, committed benchmark results — organized for future readers.
RESULTS_DIR = BENCH_DIR / "results"
RESULTS_DIR.mkdir(parents=True, exist_ok=True)
SCORE_JSONL = BENCH_DIR / "scoreboard.jsonl"
SCORE_MD = BENCH_DIR / "scoreboard.md"

REVIEW_CONCURRENCY = int(os.getenv("CAMPAIGN_CONCURRENCY", "3"))
DEPTH = os.getenv("CAMPAIGN_DEPTH", "deep")
MAX_COST = float(os.getenv("CAMPAIGN_MAX_COST", "25"))
MAX_DURATION = int(os.getenv("CAMPAIGN_MAX_DURATION", "3600"))
POLL_EVERY = 20
POLL_TIMEOUT_MIN = 80


# --------------------------------------------------------------------------- #
# PR-AF runner: fire + poll
# --------------------------------------------------------------------------- #
async def fire(client: httpx.AsyncClient, pr_url: str) -> str:
    body = {
        "input": {
            "pr_url": pr_url,
            "depth": DEPTH,
            "dry_run": True,
            "max_cost_usd": MAX_COST,
            "max_duration_seconds": MAX_DURATION,
            "max_review_depth": 2,
        }
    }
    r = await client.post(f"{CP}/api/v1/execute/async/pr-af.review", json=body, timeout=30)
    r.raise_for_status()
    return r.json()["execution_id"]


async def poll(client: httpx.AsyncClient, eid: str, label: str) -> dict:
    start = time.time()
    last = None
    while True:
        await asyncio.sleep(POLL_EVERY)
        mins = (time.time() - start) / 60
        try:
            r = await client.get(f"{CP}/api/ui/v1/executions/{eid}/details", timeout=30)
            data = r.json()
        except Exception as exc:  # noqa: BLE001
            print(f"[{label}] poll err {exc!r}", flush=True)
            continue
        st = data.get("status")
        if st != last:
            print(f"[{label}] {mins:4.1f}m status={st}", flush=True)
            last = st
        if st in ("succeeded", "completed", "failed", "cancelled", "error"):
            return data
        if mins > POLL_TIMEOUT_MIN:
            print(f"[{label}] timeout {POLL_TIMEOUT_MIN}m", flush=True)
            return data


# --------------------------------------------------------------------------- #
# Judge: recall of golden comments (severity-agnostic)
# --------------------------------------------------------------------------- #
JUDGE_SYS = (
    "You score an automated code-review tool against a PR's ground-truth GOLDEN "
    "comments. For EACH golden, decide whether ANY of the tool's findings identifies "
    "the SAME underlying issue: same code location AND same root problem. A finding "
    "that merely touches the same file but describes a different issue is NOT a match. "
    "IGNORE severity labels entirely — a match is purely about whether the bug was "
    "FOUND. Be strict but fair: paraphrases and different framings of the same defect "
    "DO match. Output STRICT JSON only:\n"
    '{"matches":[{"golden_idx":<int>,"hit":<bool>,"matched_finding":"<title or null>",'
    '"reason":"<short>"}],"recall":<hits/total float>}'
)


def _findings_blob(findings: list[dict]) -> str:
    out = []
    for i, f in enumerate(findings[:30]):
        body = (f.get("body") or "")[:600]
        sug = (f.get("suggestion") or "")[:300]
        out.append(
            f"[F{i}] ({f.get('severity')}) {f.get('file_path')}:{f.get('line_start')}\n"
            f"  title: {f.get('title')}\n  body: {body}\n  fix: {sug}"
        )
    return "\n".join(out) if out else "(no findings)"


def _goldens_blob(goldens: list[dict]) -> str:
    return "\n".join(
        f"[G{i}] ({g.get('severity')}) {g.get('comment')}" for i, g in enumerate(goldens)
    )


async def judge(client: httpx.AsyncClient, goldens: list[dict], findings: list[dict]) -> dict:
    user = (
        f"## GOLDEN COMMENTS ({len(goldens)})\n{_goldens_blob(goldens)}\n\n"
        f"## TOOL FINDINGS ({len(findings)})\n{_findings_blob(findings)}\n\n"
        "Return the JSON verdict. One entry per golden, golden_idx matching [G#]."
    )
    r = await client.post(
        "https://openrouter.ai/api/v1/chat/completions",
        headers={"Authorization": f"Bearer {OR_KEY}"},
        json={
            "model": JUDGE_MODEL,
            "messages": [{"role": "system", "content": JUDGE_SYS}, {"role": "user", "content": user}],
            "response_format": {"type": "json_object"},
            "temperature": 0,
        },
        timeout=120,
    )
    r.raise_for_status()
    txt = r.json()["choices"][0]["message"]["content"]
    s, e = txt.find("{"), txt.rfind("}")
    data = json.loads(txt[s : e + 1])
    matches = data.get("matches", [])
    hits = sum(1 for m in matches if m.get("hit"))
    total = len(goldens)
    return {
        "hits": hits,
        "total": total,
        "recall": round(hits / total, 3) if total else 0.0,
        "matches": matches,
    }


# --------------------------------------------------------------------------- #
# Per-problem pipeline
# --------------------------------------------------------------------------- #
# One review per repo at a time: the runner clones each repo into a single shared
# PR_AF_WORKDIR/<repo> dir and does `git checkout -B pr-review FETCH_HEAD` there.
# Two concurrent same-repo reviews would clobber each other's checkout and review
# the wrong tree. Hold the repo lock for the whole fire+poll (the reviewers read
# files from that checked-out tree the entire time).
_REPO_LOCKS: dict[str, asyncio.Lock] = {}


def _repo_lock(repo: str) -> asyncio.Lock:
    if repo not in _REPO_LOCKS:
        _REPO_LOCKS[repo] = asyncio.Lock()
    return _REPO_LOCKS[repo]


def _done_ids() -> set[str]:
    """Only a SUCCESSFULLY scored problem counts as done — failed/errored ones
    retry on the next run (e.g. after an infra fix + restart)."""
    if not SCORE_JSONL.exists():
        return set()
    ids = set()
    for line in SCORE_JSONL.read_text().splitlines():
        try:
            r = json.loads(line)
            if r.get("status") == "scored":
                ids.add(r["id"])
        except Exception:  # noqa: BLE001
            pass
    return ids


def _best_recall(pid: str) -> float | None:
    """Highest recall already recorded for pid (best-of guard), or None."""
    if not SCORE_JSONL.exists():
        return None
    best = None
    for line in SCORE_JSONL.read_text().splitlines():
        try:
            r = json.loads(line)
            if r.get("id") == pid and r.get("status") == "scored":
                rc = float(r.get("recall", 0.0))
                best = rc if best is None else max(best, rc)
        except Exception:  # noqa: BLE001
            pass
    return best


async def process(client: httpx.AsyncClient, sem: asyncio.Semaphore, prob: dict) -> None:
    pid = prob["id"]
    run_file = RUNS / f"{pid.replace('/', '_')}.json"

    # 1. obtain the raw review. A cache file exists ONLY for a SUCCESSFUL run
    #    (we never cache failures), so its presence means "reuse". Otherwise
    #    fire+poll while holding the per-repo lock (acquired BEFORE the global
    #    sem so a task waiting on a busy repo doesn't occupy a throughput slot).
    if run_file.exists():
        details = json.loads(run_file.read_text())
        print(f"[{pid}] using cached run", flush=True)
    else:
        async with _repo_lock(prob["repo"]), sem:
            try:
                eid = await fire(client, prob["pr_url"])
                print(f"[{pid}] fired {eid}", flush=True)
                details = await poll(client, eid, pid)
            except Exception as exc:  # noqa: BLE001
                _record({"id": pid, "repo": prob["repo"], "status": "fire_error",
                         "error": repr(exc)[:300], "difficulty": prob.get("difficulty_score"),
                         "n_goldens": len(prob["goldens"]), "hits": 0, "recall": 0.0})
                return
        # Cache ONLY successful runs so a transient failure retries next time.
        if details.get("status") in ("succeeded", "completed") and isinstance(details.get("output_data"), dict):
            run_file.write_text(json.dumps(details, default=str))

    status = details.get("status")
    out = details.get("output_data") or {}
    findings = out.get("findings") or []
    if status not in ("succeeded", "completed") or not isinstance(out, dict):
        _record({"id": pid, "repo": prob["repo"], "status": f"run_{status}",
                 "error": (details.get("error_message") or "")[:300],
                 "difficulty": prob.get("difficulty_score"), "n_goldens": len(prob["goldens"]),
                 "n_findings": len(findings), "hits": 0, "recall": 0.0})
        return

    # 2. judge (cheap, no repo lock / sem)
    try:
        verdict = await judge(client, prob["goldens"], findings)
    except Exception as exc:  # noqa: BLE001
        _record({"id": pid, "repo": prob["repo"], "status": "judge_error",
                 "error": repr(exc)[:300], "difficulty": prob.get("difficulty_score"),
                 "n_goldens": len(prob["goldens"]), "n_findings": len(findings),
                 "hits": 0, "recall": 0.0})
        return

    rec = {
        "id": pid, "repo": prob["repo"], "status": "scored",
        "difficulty": prob.get("difficulty_score"),
        "n_goldens": verdict["total"], "n_findings": len(findings),
        "hits": verdict["hits"], "recall": verdict["recall"],
        "missed": [prob["goldens"][m["golden_idx"]]["comment"][:120]
                   for m in verdict["matches"] if not m.get("hit")
                   and 0 <= m.get("golden_idx", -1) < len(prob["goldens"])],
        "cost_usd": (out.get("summary") or {}).get("cost_usd"),
        "duration_s": (out.get("summary") or {}).get("duration_seconds"),
    }

    # BEST-OF guard: a noisy re-run must never overwrite a better prior result.
    prior = _best_recall(pid)
    if prior is not None and verdict["recall"] < prior:
        print(f"[{pid}] kept prior recall={prior} over rerun {verdict['recall']} (best-of)", flush=True)
        return

    # 3. curated, committed per-problem result for future readers
    result_doc = {
        "id": pid,
        "repo": prob["repo"],
        "pr_url": prob["pr_url"],
        "language": prob.get("language"),
        "review_model": REVIEW_MODEL,
        "judge_model": JUDGE_MODEL,
        "difficulty_score": prob.get("difficulty_score"),
        "recall": verdict["recall"],
        "hits": verdict["hits"],
        "n_goldens": verdict["total"],
        "duration_seconds": rec["duration_s"],
        "cost_usd": rec["cost_usd"],
        "goldens": prob["goldens"],
        "golden_verdicts": verdict["matches"],
        # EXACT, untrimmed findings — every field, full bodies/evidence/suggestions.
        "findings": findings,
    }
    (RESULTS_DIR / f"{pid.replace('/', '_')}.json").write_text(
        json.dumps(result_doc, indent=2, default=str)
    )
    _record(rec)
    print(f"[{pid}] SCORED recall={rec['recall']} ({rec['hits']}/{rec['n_goldens']})", flush=True)


def _record(rec: dict) -> None:
    with SCORE_JSONL.open("a") as f:
        f.write(json.dumps(rec, default=str) + "\n")
    _render()


def _render() -> None:
    raw = [json.loads(line) for line in SCORE_JSONL.read_text().splitlines() if line.strip()]
    # Dedup by id: a scored row always wins over a failed one; later wins over earlier.
    by_id: dict[str, dict] = {}
    for r in raw:
        cur = by_id.get(r["id"])
        if cur is None or r.get("status") == "scored" or cur.get("status") != "scored":
            by_id[r["id"]] = r
    rows = list(by_id.values())
    scored = [r for r in rows if r.get("status") == "scored"]
    tot_g = sum(r["n_goldens"] for r in scored)
    tot_h = sum(r["hits"] for r in scored)
    micro = round(tot_h / tot_g, 3) if tot_g else 0.0
    macro = round(sum(r["recall"] for r in scored) / len(scored), 3) if scored else 0.0
    lines = [
        "# GLM-5.2 + PR-AF — Martian Code-Review-Bench scoreboard",
        "",
        f"Scored {len(scored)} problems · micro-recall {tot_h}/{tot_g} = **{micro}** · "
        f"macro-recall **{macro}** · judge={JUDGE_MODEL} · severity-agnostic (HIT = bug found)",
        "",
        "| problem | repo | diff | goldens | hits | recall | findings | missed |",
        "|---|---|---|---|---|---|---|---|",
    ]
    for r in sorted(rows, key=lambda x: -(x.get("difficulty") or 0)):
        if r.get("status") != "scored":
            lines.append(f"| {r['id']} | {r['repo']} | {r.get('difficulty')} | "
                         f"— | — | _{r.get('status')}_ | — | {r.get('error','')[:60]} |")
            continue
        missed = "; ".join(r.get("missed", []))[:80] or "—"
        lines.append(f"| {r['id']} | {r['repo']} | {r.get('difficulty')} | {r['n_goldens']} | "
                     f"{r['hits']} | **{r['recall']}** | {r.get('n_findings')} | {missed} |")
    SCORE_MD.write_text("\n".join(lines) + "\n")


async def main() -> None:
    # problems.json is sorted hardest-first. Process in that order; CAMPAIGN_LIMIT
    # caps how many UNSOLVED problems this invocation runs (token-frugal batching:
    # go a couple at a time, inspect, fix-and-rerun on failure, then continue).
    problems = [p for p in json.loads(PROBLEMS.read_text()) if not p.get("pr_url_uncertain")]
    done = _done_ids()
    force = {x.strip() for x in os.getenv("CAMPAIGN_FORCE", "").split(",") if x.strip()}
    todo = [p for p in problems if p["id"] not in done or p["id"] in force]
    limit = int(os.getenv("CAMPAIGN_LIMIT", "0"))
    if limit > 0:
        todo = todo[:limit]
    print(f"[campaign] {len(problems)} runnable, {len(done)} already scored, "
          f"running {len(todo)} this batch; concurrency={REVIEW_CONCURRENCY} depth={DEPTH}", flush=True)
    sem = asyncio.Semaphore(REVIEW_CONCURRENCY)
    async with httpx.AsyncClient() as client:
        await asyncio.gather(*(process(client, sem, p) for p in todo))
    print("[campaign] done", flush=True)


if __name__ == "__main__":
    asyncio.run(main())
