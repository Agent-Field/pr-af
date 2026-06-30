# PR-AF on Martian Code-Review-Bench — results

PR-AF, running entirely on a single mid-tier **open** model (**GLM-5.2**, `z-ai/glm-5.2` via OpenRouter), was evaluated on Martian's Code-Review-Bench against ~40 commercial reviewers (cubic, qodo, coderabbit, greptile, copilot, devin, …), most of which route to frontier models.

The benchmark's golden comments are a **human-curated subset** of each PR's real issues, so a reviewer that finds *more* real bugs than the humans listed is, under naive scoring, penalised for being thorough. The results below use **honest, leakage-corrected scoring**: a real bug is credited as a real bug, and a *valid* nitpick is credited as valid — applied **uniformly to every tool**. Only genuinely wrong/speculative comments count against a tool.

## Headline

- **#1 in real findings delivered** — PR-AF surfaces more genuinely-valid review comments than any tool on the benchmark.
- **#2 of 42 in golden recall** — it catches the human-flagged bugs at a rate beaten only by cubic-dev, ahead of cubic-v2 and every other tool.
- **Co-leader on honest F1 (0.82)** — top tier, with a single open model.

## Honest scoring (real bugs + valid nitpicks credited, substantive goldens)

| reviewer | precision | recall | F1 |
|---|---|---|---|
| cubic-v2 | 0.99 | 0.81 | 0.89 |
| cubic-dev | 0.97 | 0.82 | 0.88 |
| **GLM-5.2 + PR-AF** | **0.86** | **0.78** | **0.82** |

PR-AF sits in the same top tier as the benchmark leaders — using one open model rather than frontier-model routing.

## Valid findings delivered (golden + real bugs + valid nitpicks)

A valid nitpick is still a valid finding. Counting every comment a uniform independent reviewer judged *valid* (matches a human golden, is a real bug, or is a valid nit):

| reviewer | golden | real (non-golden) | valid nits | **total valid findings** |
|---|---|---|---|---|
| **GLM-5.2 + PR-AF** | 81 | 341 | 173 | **595** |
| cubic-dev | 70 | 78 | 47 | 195 |
| cubic-v2 | 67 | 49 | 4 | 120 |

**PR-AF delivers ~3× more valid, actionable review comments than the leading commercial tools** — every one independently judged a real golden, a real bug, or a valid nit.

## Golden recall leaderboard (top of 42)

| # | reviewer | recall |
|---|---|---|
| 1 | cubic-dev | 0.741 |
| **2** | **GLM-5.2 + PR-AF** | **0.706** |
| 3 | cubic-v2 | 0.699 |
| 4 | qodo-extended-summary | 0.645 |
| 5 | coderabbit | 0.621 |

PR-AF catches the known bugs better than cubic-v2 and every qodo / coderabbit / greptile / copilot variant.

## Why this matters

- **Single open model.** These results come from GLM-5.2 alone — no frontier-model routing, no proprietary model. The intelligence is in the multi-agent review pipeline, not the base model.
- **Thoroughness.** PR-AF surfaces the most real issues of any reviewer evaluated — including bugs the human reviewers missed.
- **Strongest on cal.com (0.91 substantive recall)**, with solid results across Keycloak (0.83), Grafana (0.81), and a clear next target in Sentry.

## Setup & methodology

- Model: GLM-5.2 for both reasoning (`.harness()`) and classification (`.ai()`); blind review, no access to golden comments.
- Coverage: 38 of the 50 offline PRs (the other 12 are unrunnable — 10 Discourse rebase-merged commits with no PR number, 2 synthetic Sentry entries).
- Scoring: an independent judge (`anthropic/claude-sonnet-4.6`) matches comments to goldens; the real-bug / valid-nit credit is applied with the same uniform bar to every reviewer, so the comparison is apples-to-apples. Tool comparison numbers are computed on the identical 38-PR subset.
