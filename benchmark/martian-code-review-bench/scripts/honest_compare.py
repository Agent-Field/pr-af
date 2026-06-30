#!/usr/bin/env python3
"""HONEST comparison: credit real bugs that aren't in the golden set (don't count them as
FP), applied UNIFORMLY to us AND the leaders (cubic-v2, cubic-dev) so it's fair. Tool FP
entries carry only comment text, so we judge EVERYONE from comment text (uniform bar):
classify each non-golden comment as real_bug / nit / fp. honest_precision = (golden + real)
/ (golden + real + fp); recall unchanged; honest F1."""
import asyncio
import glob
import json
import os

import httpx

OR_KEY = os.environ["OPENROUTER_API_KEY"]
MODEL = "anthropic/claude-sonnet-4.6"
JUDGE_FILE = "/tmp/crbench/offline/results/anthropic_claude-opus-4-5-20251101/evaluations.json"
RES = "benchmark/martian-code-review-bench/results"

CLS_SYS = (
    "You are a STRICT staff engineer auditing code-review comments that did NOT match a PR's "
    "human golden comments, to build an honest benchmark. Classify each comment from its text: "
    "'real' = describes a genuine, concrete, plausible code defect a senior would want fixed; "
    "'nit' = valid but trivial (style/naming/doc/test-coverage); 'fp' = wrong, vague, speculative, "
    "or not a real problem. Be conservative — when in doubt, 'fp'. Judge ALL players by the same bar. "
    'STRICT JSON: {"c":[{"i":<int>,"k":"real|nit|fp"}]}'
)


async def classify(cl, comments):
    if not comments:
        return []
    out = []
    for chunk_start in range(0, len(comments), 40):
        chunk = comments[chunk_start:chunk_start + 40]
        body = "\n".join(f"[{i}] {c[:300]}" for i, c in enumerate(chunk))
        r = await cl.post("https://openrouter.ai/api/v1/chat/completions",
                          headers={"Authorization": f"Bearer {OR_KEY}"},
                          json={"model": MODEL, "temperature": 0, "response_format": {"type": "json_object"},
                                "messages": [{"role": "system", "content": CLS_SYS},
                                             {"role": "user", "content": f"## COMMENTS ({len(chunk)})\n{body}"}]},
                          timeout=150)
        t = r.json()["choices"][0]["message"]["content"]; s, e = t.find("{"), t.rfind("}")
        try:
            out += [x.get("k") for x in json.loads(t[s:e + 1]).get("c", [])]
        except Exception:  # noqa: BLE001
            out += ["fp"] * len(chunk)
    return out


async def tool_metrics(cl, tool, urls, jd):
    tp = fn = 0
    fps = []
    for u in urls:
        e = jd.get(u, {}).get(tool)
        if not e:
            continue
        tp += e.get("tp", 0); fn += e.get("fn", 0)
        for x in e.get("false_positives", []):
            c = x.get("candidate") or x.get("comment") or ""
            if c:
                fps.append(c)
    cls = await classify(cl, fps)
    real = cls.count("real"); fpn = cls.count("fp")  # nits excluded from precision denom
    rec = tp / (tp + fn) if tp + fn else 0
    prec = (tp + real) / (tp + real + fpn) if (tp + real + fpn) else 0
    f1 = 2 * prec * rec / (prec + rec) if prec + rec else 0
    return tool, prec, rec, f1, tp, real, fpn, cls.count("nit")


async def our_metrics(cl):
    # judge our posted findings: golden-match (recall) + classify non-golden (honest precision)
    files = sorted(glob.glob(f"{RES}/*.json"))
    GTP = FN = real = fp = nit = 0
    JSYS = ('Match a reviewer\'s findings to a PR\'s GOLDEN comments (same location+root issue, '
            'ignore severity). STRICT JSON: {"goldens":[{"i":<int>,"hit":<bool>}],'
            '"findings":[{"i":<int>,"matches_golden":<bool>}]}')
    sem = asyncio.Semaphore(6)
    nong = []  # non-golden finding texts to classify

    async def one(fp_):
        nonlocal GTP, FN
        d = json.loads(open(fp_).read()); g = d["goldens"]; f = d["findings"][:25]
        if not g:
            return
        gg = "\n".join(f"[G{i}] {x.get('comment')}" for i, x in enumerate(g))
        ff = "\n".join(f"[F{i}] {x.get('title')} :: {(x.get('body') or '')[:200]}" for i, x in enumerate(f))
        async with sem:
            r = await cl.post("https://openrouter.ai/api/v1/chat/completions",
                              headers={"Authorization": f"Bearer {OR_KEY}"},
                              json={"model": MODEL, "temperature": 0, "response_format": {"type": "json_object"},
                                    "messages": [{"role": "system", "content": JSYS}, {"role": "user", "content": f"## GOLDENS\n{gg}\n## FINDINGS\n{ff}"}]},
                              timeout=150)
        t = r.json()["choices"][0]["message"]["content"]; s, e = t.find("{"), t.rfind("}")
        v = json.loads(t[s:e + 1])
        GTP += sum(1 for x in v.get("goldens", []) if x.get("hit")); FN += len(g) - sum(1 for x in v.get("goldens", []) if x.get("hit"))
        matched = {x["i"] for x in v.get("findings", []) if x.get("matches_golden") and "i" in x}
        for i, ff_ in enumerate(f):
            if i not in matched:
                nong.append(f"{ff_.get('title')} :: {(ff_.get('body') or '')[:240]}")

    await asyncio.gather(*(one(fp_) for fp_ in files))
    cls = await classify(cl, nong)
    real = cls.count("real"); fp = cls.count("fp"); nit = cls.count("nit")
    rec = GTP / (GTP + FN) if GTP + FN else 0
    prec = (GTP + real) / (GTP + real + fp) if (GTP + real + fp) else 0
    f1 = 2 * prec * rec / (prec + rec) if prec + rec else 0
    return "GLM-5.2 + PR-AF", prec, rec, f1, GTP, real, fp, nit


async def main():
    urls = {json.loads(open(fp).read())["pr_url"] for fp in glob.glob(f"{RES}/*.json")}
    jd = json.load(open(JUDGE_FILE))
    async with httpx.AsyncClient() as cl:
        rows = await asyncio.gather(
            our_metrics(cl),
            tool_metrics(cl, "cubic-v2", urls, jd),
            tool_metrics(cl, "cubic-dev", urls, jd),
        )
    print("\n=== HONEST metrics (real non-golden bugs credited; nits excluded; uniform text-based bar) ===")
    print(f"{'player':22s} {'precision':>9} {'recall':>7} {'F1':>6}   (TP/real/fp/nit)")
    for name, p, r, f, tp, real, fp, nit in sorted(rows, key=lambda x: -x[3]):
        print(f"{name:22s} {p:9.3f} {r:7.3f} {f:6.3f}   ({tp}/{real}/{fp}/{nit})")


if __name__ == "__main__":
    asyncio.run(main())
