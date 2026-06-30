#!/usr/bin/env python3
"""Golden-only: compute our posted-comment precision/recall/F1 and rank us on EACH
metric vs all leaderboard tools (same posted-comment basis they're scored on)."""
import asyncio
import glob
import json
import os
import re
import subprocess

import httpx

OR_KEY = os.environ["OPENROUTER_API_KEY"]
MODEL = "anthropic/claude-sonnet-4.6"
RES = "benchmark/martian-code-review-bench/results"
EVAL = "/tmp/crbench/offline/results"
SYS = ('Score a reviewer vs a PR\'s GOLDEN comments, both directions. Match = same code location '
       'AND same root issue (paraphrases match). IGNORE severity. STRICT JSON: '
       '{"goldens":[{"i":<int>,"hit":<bool>}],"findings":[{"i":<int>,"matches_golden":<bool>}]}')


def ranges(o, r, n):
    d = subprocess.run(["gh", "pr", "diff", n, "--repo", f"{o}/{r}"], capture_output=True, text=True, timeout=90).stdout
    rg, cur = {}, None
    for ln in d.splitlines():
        if ln.startswith("diff --git"):
            m = re.search(r" b/(.+)$", ln); cur = m.group(1) if m else None
        elif ln.startswith("@@") and cur:
            m = re.search(r"\+(\d+)(?:,(\d+))?", ln)
            if m:
                s = int(m.group(1)); k = int(m.group(2) or "1"); rg.setdefault(cur, []).append((s, s + k))
    return rg


def indiff(f, rg):
    fp = f.get("file_path", "")
    key = next((k for k in rg if k == fp or k.endswith("/" + fp) or fp.endswith("/" + k) or k.split("/")[-1] == fp.split("/")[-1]), None)
    return bool(key) and any(a <= f.get("line_start", 0) <= b for a, b in rg[key])


async def judge(cl, g, fs):
    gg = "\n".join(f"[G{i}] {x.get('comment')}" for i, x in enumerate(g))
    ff = "\n".join(f"[F{i}] {x.get('file_path')}:{x.get('line_start')} {x.get('title')} :: {(x.get('body') or '')[:200]}" for i, x in enumerate(fs))
    r = await cl.post("https://openrouter.ai/api/v1/chat/completions",
                      headers={"Authorization": f"Bearer {OR_KEY}"},
                      json={"model": MODEL, "temperature": 0, "response_format": {"type": "json_object"},
                            "messages": [{"role": "system", "content": SYS}, {"role": "user", "content": f"## GOLDENS\n{gg}\n\n## FINDINGS\n{ff or '(none)'}"}]},
                      timeout=150)
    t = r.json()["choices"][0]["message"]["content"]; s, e = t.find("{"), t.rfind("}")
    return json.loads(t[s:e + 1])


async def main():
    files = sorted(glob.glob(f"{RES}/*.json"))
    sem = asyncio.Semaphore(6)
    TPg = FN = TPf = FP = 0

    async def one(fp):
        nonlocal TPg, FN, TPf, FP
        d = json.loads(open(fp).read()); g = d["goldens"]
        if not g:
            return
        m = re.search(r"github\.com/([^/]+)/([^/]+)/pull/(\d+)", d["pr_url"])
        try:
            rg = ranges(m.group(1), m.group(2), m.group(3))
        except Exception:
            rg = {}
        posted = [f for f in d["findings"] if indiff(f, rg)] if rg else d["findings"][:12]
        async with sem:
            async with httpx.AsyncClient() as cl:
                try:
                    v = await judge(cl, g, posted)
                except Exception as exc:
                    print("err", d["id"], repr(exc)[:40]); return
        gh = sum(1 for x in v.get("goldens", []) if x.get("hit"))
        fm = sum(1 for x in v.get("findings", []) if x.get("matches_golden"))
        ft = len(v.get("findings", [])) or len(posted)
        TPg += gh; FN += len(g) - gh; TPf += fm; FP += ft - fm

    await asyncio.gather(*(one(fp) for fp in files))
    rec = TPg / (TPg + FN) if TPg + FN else 0
    pre = TPf / (TPf + FP) if TPf + FP else 0
    f1 = 2 * pre * rec / (pre + rec) if pre + rec else 0
    urls = {json.loads(open(fp).read())["pr_url"] for fp in files}
    judges = [json.load(open(p)) for p in glob.glob(f"{EVAL}/*/evaluations.json")]
    tools = {}
    for jd in judges:
        for u in urls:
            for t, e in jd.get(u, {}).items():
                a = tools.setdefault(t, [0, 0, 0]); a[0] += e.get("tp", 0); a[1] += e.get("fp", 0); a[2] += e.get("fn", 0)
    P = {t: (tp / (tp + fp) if tp + fp else 0) for t, (tp, fp, fn) in tools.items()}; P["US"] = pre
    R = {t: (tp / (tp + fn) if tp + fn else 0) for t, (tp, fp, fn) in tools.items()}; R["US"] = rec
    F = {t: (2 * P[t] * R[t] / (P[t] + R[t]) if P[t] + R[t] else 0) for t in tools}; F["US"] = f1
    rk = lambda d: sorted(d, key=lambda k: -d[k]).index("US") + 1
    N = len(tools) + 1
    print(f"\n=== GLM-5.2 + PR-AF, golden-only (posted-comment basis), micro over {len(files)} PRs ===")
    print(f"recall    = {rec:.3f}  -> #{rk(R)} of {N}")
    print(f"precision = {pre:.3f}  -> #{rk(P)} of {N}")
    print(f"F1        = {f1:.3f}  -> #{rk(F)} of {N}")
    json.dump({"recall": rec, "precision": pre, "f1": f1, "ranks": {"recall": rk(R), "precision": rk(P), "f1": rk(F)}},
              open("_glm52_bench/golden_metrics.json", "w"), indent=2)
    for name, d in [("F1", F), ("RECALL", R), ("PRECISION", P)]:
        print(f"\n--- {name} top 8 ---")
        for i, (t, v) in enumerate(sorted(d.items(), key=lambda x: -x[1])[:8], 1):
            print(f"{i:2d}. {t:24s} {v:.3f}{'  <===' if t=='US' else ''}")


if __name__ == "__main__":
    asyncio.run(main())
