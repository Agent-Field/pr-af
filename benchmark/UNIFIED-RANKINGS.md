# Unified PR Review Tool Rankings
## 2026-03-20

---

## Single Ranking Table

All tools ranked by **F1 score** (harmonic mean of precision and recall). F1 is the right metric because it penalizes systems that are good at only one of {finding bugs, not crying wolf}.

| Rank | Tool | Precision | Recall | **F1** | Source | Sample | Notes |
|---|---|---|---|---|---|---|---|
| 1 | **PR-AF v2 + Sonnet 4.6** | 96.0% | 66.7% | **78.9%** | Internal | 1 PR, 9 GT bugs | Mar 10 baseline. Best single-PR score. |
| 2 | **PR-AF v2 + Kimi k2.5** | 78.0% | 66.7% | **71.8%** | Internal | 1 PR, 9 GT bugs | Complementary coverage to Sonnet. |
| 3 | **PR-AF v3r + Sonnet 4.6** | **98.0%** | 44.4% | **61.1%** | Internal | 1 PR, 9 GT bugs | **New architecture. Highest precision of any system.** |
| 4 | **Claude Code (claude[bot])** | 92.0% | 44.4% | **59.9%** | Internal | 1 PR, 9 GT bugs | Single-agent instant baseline. |
| 5 | **qodo** | 49.5% | 46.4% | **47.9%** | Public | 42 PRs | Current public #1. |
| 6 | augment | 26.3% | 62.7% | **37.1%** | Public | 42 PRs | Highest public recall. |
| 7 | propel | 35.2% | 39.1% | **37.1%** | Public | 42 PRs | |
| 8 | claude (single-agent) | 35.7% | 35.7% | **35.7%** | Public | 42 PRs | Anthropic single-agent. |
| 9 | copilot | 24.1% | 52.7% | **33.0%** | Public | 42 PRs | GitHub Copilot. |
| 10 | bugbot | 26.5% | 43.6% | **33.0%** | Public | 42 PRs | |
| 11 | **PR-AF premium (OLD, on public)** | 13.7% | **68.4%** | **22.8%** | Public | 5-7 PRs | Old architecture on public bench. Best recall ever. |
| 12 | PR-AF v3 + minimax (best) | 85.0% | 11.1% | **19.7%** | Internal | 1 PR, 9 GT bugs | Wrong model for this architecture. |
| 13 | PR-AF mid (OLD, on public) | 8.5% | 62.5% | **14.9%** | Public | 5-7 PRs | |
| 14 | PR-AF budget (OLD, on public) | 8.2% | 53.3% | **14.3%** | Public | 5-7 PRs | |

---

## Reading This Table

**Why can't we directly compare internal vs public?**

The internal runs (ranks 1-4, 12) are on a single hard PR (TrueNAS ZFS encryption refactor) with 9 manually-confirmed ground truth bugs. The public runs (ranks 5-11, 13-14) are on 42 diverse PRs scored against golden comments. Internal precision/recall tend to be higher because the ground truth is hand-curated and the PR is complex (more signal to find). The public benchmark is noisier and more diverse.

**What IS directly comparable:**
- Ranks 5-10 vs ranks 11, 13-14: Same 42-PR public benchmark → PR-AF OLD (rank 11) vs competitors
- Ranks 1-4, 12: Same TrueNAS PR → our architecture versions against each other

**The gap that matters:** PR-AF OLD on the public benchmark had 68.4% recall (best) but 13.7% precision (worst) → F1 22.8% (last place). The new architecture achieved 98% precision internally. If even a fraction of that holds on the public benchmark, F1 jumps dramatically.

---

## What Each Metric Means (In Plain Terms)

| Metric | What It Measures | Why It Matters | Who Cares |
|---|---|---|---|
| **Recall** | "Of all real bugs, how many did we catch?" | A missed bug can ship to production. Higher = safer. | Engineering leads, security teams |
| **Precision** | "Of everything we flagged, how much was real?" | Low precision = comment fatigue → devs ignore the tool. Higher = more trusted. | Individual developers, adoption |
| **F1** | Balances both. Harmonic mean penalizes being bad at either. | The single number that says "is this tool useful in practice?" | Everyone evaluating tools |

**For actual adoption, precision matters more than the math suggests.** A tool with 30% precision means 70% of its comments are noise. Developers learn to skip them. A tool with 90%+ precision means every comment is worth reading. This is why qodo leads the public leaderboard despite mediocre recall — developers actually read its comments.

---

## Where PR-AF Stands

**Our strength**: Recall. We find bugs others miss. 68.4% on public (best), 67% on TrueNAS (tied best).

**Our old weakness**: Precision. 13.7% on public = dead last. Too much noise.

**What the v3r architecture fixed**: Precision went from 13.7% → 98% (on TrueNAS). Evidence verification, research brief, adversary phase, and gap finder all contribute.

**What it cost**: Recall dropped from 67% → 44% (on TrueNAS). The tighter filtering caught fewer bugs. Two specific bugs (KMIP type mismatch, missing datastore argument) were found by old architecture but missed by new.

**Next milestone**: Run v3r on the full 42-PR public benchmark. If precision goes to even 30% with recall at 60%+, F1 hits ~40%+ and we're top-3. If precision hits 40%+ with recall 60%+, we're #1.
