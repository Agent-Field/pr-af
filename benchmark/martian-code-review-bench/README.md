# PR-AF on the Martian Code-Review-Bench

PR-AF's results on [Martian's Code-Review-Bench v0](https://codereview.withmartian.com/)
([repo](https://github.com/withmartian/code-review-benchmark)), run end-to-end on a single
mid-tier **open** model: **GLM-5.2** (`openrouter/z-ai/glm-5.2`). The same 50 merged PRs
(5 repos: cal.com, Discourse, Grafana, Keycloak, Sentry) that the Martian leaderboard scores
commercial reviewers (cubic, qodo, coderabbit, greptile, …) on, most of which route to
frontier models.

The point: show what the PR-AF multi-agent architecture extracts from one open model on real,
hard PRs — the intelligence is in the composition, not the base model.

## Headline

See [`RESULTS.md`](RESULTS.md) for the full results. In short, on the 38 runnable PRs:

- **#1 in valid findings delivered** — ~3× more independently-judged-valid review comments than
  the leading commercial tools.
- **#2 of 42 in golden recall** (0.706) — beaten only by cubic-dev; ahead of cubic-v2 and every
  qodo / coderabbit / greptile / copilot variant.
- **Co-leader on honest F1 (0.82)** — top tier, with a single open model.

## What's here

| path | contents |
|---|---|
| [`RESULTS.md`](RESULTS.md) | The headline results (honest, leakage-corrected scoring). |
| [`scoreboard.md`](scoreboard.md) | Live aggregate recall table, sorted hardest-first. |
| `scoreboard.jsonl` | Machine-readable, one line per scored problem. |
| `problems.json` | The ranked worklist: 50 PRs + golden comments + difficulty scores. |
| `results/<id>.json` | Per-problem detail: the PR, its goldens, **every** PR-AF finding (exact and full — no truncation), and the judge's per-golden HIT/MISS verdict with reasons. |
| `analysis/` | Secondary views: all-golden leaderboard standing and substantive-golden scoreboard. |
| `scripts/` | Reproduction: node launcher, campaign runner, ensemble escalation, scoring. See [`scripts/README.md`](scripts/README.md). |

## Methodology

- **Model.** Both PR-AF primitives run on GLM-5.2: `.harness()` via opencode, `.ai()` via litellm.
  No other model touches a review.
- **Blind.** Each PR is reviewed with no hints — PR-AF gets only the PR, never the golden comments.
- **Depth.** `depth=deep`, `dry_run=true` (nothing is posted to GitHub). Cost uncapped for quality.
- **Difficulty.** `problems.json` is sorted by the benchmark's own authoritative miss-rate: for
  each PR, the fraction of (judge × tool) pairs in Martian's `evaluations.json` that FAILED to
  catch its goldens (3 judges × ~41 tools). Higher = harder. Range 0.20–0.93.
- **Scoring.** An independent judge (`anthropic/claude-sonnet-4.6`) decides, per golden, whether
  any PR-AF finding identifies the same underlying issue (same location + same root cause).
  `scoreboard.md` is the severity-agnostic recall view; `RESULTS.md` adds the honest precision/F1
  view that credits real non-golden bugs uniformly across all tools.

## Reproduce

See [`scripts/README.md`](scripts/README.md). From the repo root (`examples/pr-af`):

```bash
bash benchmark/martian-code-review-bench/scripts/run_node.sh           # node, pinned to GLM-5.2
uv run python benchmark/martian-code-review-bench/scripts/campaign.py  # fire + poll + judge + score
```

## Caveats

- 38 of the 50 problems are runnable as upstream PR URLs. 12 are deferred: 10 Discourse goldens
  reference a rebase-merged commit (no recoverable PR number) and 2 Sentry entries are synthetic
  (the benchmark notes "no such PR / a mix of many PRs"). These need a commit-ref review path.
- The Martian leaderboard updates over time; difficulty here is a snapshot of the cloned v0 dataset.
- Judge matching is semantic and model-driven; `results/<id>.json` records the judge's reasoning
  per golden so any call can be audited.
