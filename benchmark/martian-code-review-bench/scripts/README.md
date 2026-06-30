# Reproduction scripts

These produce the data in this directory. Run them from the **pr-af repo root**
(`examples/pr-af`). They read `OPENROUTER_API_KEY` from the repo-root `.env` (never
hardcoded). Raw per-run transcripts and caches are written to a gitignored
`_glm52_bench/` scratch dir at the repo root.

| script | what it does |
|---|---|
| `run_node.sh` | Launches the PR-AF node with the whole pipeline pinned to GLM-5.2 (`openrouter/z-ai/glm-5.2` for both `.harness()` and `.ai()`), registers with the AgentField control plane on `:8080`. |
| `campaign.py` | Fires a blind `depth=deep` review for each problem in `../problems.json` (hardest-first, resumable, one review per repo at a time), LLM-judges findings against the goldens for **recall**, and writes `../scoreboard.{md,jsonl}` + `../results/<id>.json`. |
| `ensemble.py` | Self-consistency escalation: for every baseline miss, run K extra independent passes, union the findings, re-judge. Run after `campaign.py` prints `[campaign] done`. |
| `all_metrics.py` | Golden-only precision/recall/F1 on the posted-comment basis, ranked against every leaderboard tool from the cloned Martian dataset. |
| `honest_compare.py` | Honest scoring (Framing C, see `../RESULTS.md`): credits real non-golden bugs, applied uniformly to PR-AF and the leaders (cubic-v2, cubic-dev). |

## Run

```bash
# from examples/pr-af
bash benchmark/martian-code-review-bench/scripts/run_node.sh          # terminal 1: the node
uv run python benchmark/martian-code-review-bench/scripts/campaign.py  # terminal 2: the campaign
uv run python benchmark/martian-code-review-bench/scripts/ensemble.py  # optional: miss escalation
```

`all_metrics.py` and `honest_compare.py` additionally need Martian's cloned offline
dataset at `/tmp/crbench/offline/results/<judge>/evaluations.json` (the per-tool
true/false-positive records they rank against).

## Knobs (env)

`CAMPAIGN_CONCURRENCY` (default 3) · `CAMPAIGN_DEPTH` (deep) · `CAMPAIGN_MAX_COST`
· `CAMPAIGN_MAX_DURATION` · `CAMPAIGN_LIMIT` (cap unsolved problems per invocation)
· `CAMPAIGN_FORCE` (comma-ids to re-run) · `ENSEMBLE_PASSES` (default 2).
