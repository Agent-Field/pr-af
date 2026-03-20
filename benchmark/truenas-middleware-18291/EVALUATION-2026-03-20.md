# LLM-as-a-Judge Evaluation: Architecture Redesign Impact
## truenas/middleware PR #18291 — ZFS Dataset Encryption Refactor
## 2026-03-20: Pre vs Post Architecture Redesign + Historical Comparison

**Evaluation date**: 2026-03-20
**Evaluator**: LLM-as-a-Judge (structured rubric, same methodology as 2026-03-10 EVALUATION.md)
**Architecture versions compared**:
- **Historical (Mar 10)**: PR-AF v2 meta-selector pipeline + Sonnet 4.6 / Kimi k2.5 / Claude Code
- **Pre-redesign (Mar 20)**: PR-AF v3 efficiency branch + minimax-m2.5 (3 runs)
- **Post-redesign (Mar 20)**: PR-AF v3 + architecture redesign (research brief, cross-cluster patches, gap finder) + minimax-m2.5

---

## 1. Ground Truth (9 Confirmed Bugs)

| ID | Bug | Category | Difficulty |
|---|---|---|---|
| GT-1 | `@pass_thread_local_storage` decorator dispatch crash in `sync_zfs_keys` | Cross-file contract violation | Hard |
| GT-2 | `ZFSKeyFormat` enum comparison always False (enum vs string) | Type-level runtime | Medium |
| GT-3 | `pbkdf2iters` minimum inconsistency across option classes | Cross-file contract | Medium |
| GT-4 | `k in existing_datasets` type mismatch — silently wipes KMIP cache | Type-level runtime | Hard |
| GT-5 | Method shadowing: `check_key` name shadows import, infinite recursion | Name resolution | Medium |
| GT-6 | Duplicate export: `PoolRemoveArgs` in `__all__` | Mechanical | Easy |
| GT-7 | Missing argument: `ds['id']` in `datastore.update` | Missing argument | Medium |
| GT-8 | Exception contract: broad `Exception` catch masks `ZFSNotEncryptedException` | Exception handling | Easy-Medium |
| GT-9 | TOCTOU race condition in `load_key()` | Concurrency | Medium |

---

## 2. System-by-System Results

### 2.1 Historical Baseline: PR-AF v2 + Sonnet 4.6 (Mar 10)

| Metric | Value |
|---|---|
| Model | Claude Sonnet 4.6 |
| Duration | ~35 min |
| Total findings | 14 |
| Adversary challenged | 0 (0%) |
| Weighted score | 0.828 |

| GT Bug | Found? | Finding Details |
|---|---|---|
| GT-1 | NO (investigated, ruled out) | Findings #12, #14 explicitly analyzed decorator dispatch, concluded not a bug |
| GT-2 | YES | Finding #3: enum comparison bug (score 0.686) |
| GT-3 | YES | Findings #5, #7, #11: pbkdf2iters inconsistency |
| GT-4 | YES | Finding #1: KMIP cache wipe (score 0.97, top finding) |
| GT-5 | NO | Not in any analysis dimension |
| GT-6 | NO | Not detected |
| GT-7 | YES | Finding #2: missing ds['id'] (score 0.95, novel) |
| GT-8 | YES | Findings #4, #8, #9, #10: exception hierarchy analysis |
| GT-9 | NO | Not detected |

**Recall: 6/9 (67%)**. Precision: ~96%. Found the two hardest bugs (GT-4, GT-7).

### 2.2 Historical Baseline: PR-AF v2 + Kimi k2.5 (Mar 10)

| Metric | Value |
|---|---|
| Model | Kimi k2.5 |
| Duration | ~19 min |
| Total findings | 25 |
| Adversary challenged | 7 (28%) |
| Weighted score | 0.727 |

| GT Bug | Found? | Finding Details |
|---|---|---|
| GT-1 | NO | Not in any analysis dimension |
| GT-2 | NO | Not detected |
| GT-3 | YES | Findings #6, #7, #8: pbkdf2iters |
| GT-4 | NO | Not detected |
| GT-5 | YES | Finding #1: method shadowing (score 1.852, highest across all systems) |
| GT-6 | YES | Finding #3: duplicate export |
| GT-7 | NO | Not detected |
| GT-8 | YES | Findings #2, #11, #12, #13: exception handling |
| GT-9 | YES | Finding #5: TOCTOU race |

**Recall: 6/9 (67%)**. Precision: ~78%. Found GT-5 (unique) and GT-9 (unique).

### 2.3 Historical Baseline: Claude Code (Mar 10)

| Metric | Value |
|---|---|
| Model | claude[bot] single-agent |
| Duration | Near-instant |
| Total findings | ~6 |
| Weighted score | 0.656 |

| GT Bug | Found? |
|---|---|
| GT-1 | YES (unique — only system to find this) |
| GT-2 | YES |
| GT-3 | YES |
| GT-4 | YES |
| GT-5 | NO |
| GT-6 | NO |
| GT-7 | NO |
| GT-8 | NO |
| GT-9 | NO |

**Recall: 4/9 (44%)**. Precision: ~92%.

### 2.4 Pre-Redesign: PR-AF v3 + minimax-m2.5 (Mar 20, Best of 3 Runs)

Three runs were performed. Run B (minimax-m2.7) produced 0 findings due to model permission error. Runs A and C are scored.

**Run A** (exec_20260320_073251_dwhg6a3q):
| Metric | Value |
|---|---|
| Duration | 2449s (~41 min) |
| Dimensions | 6 |
| Total findings | 5 (2 critical, 3 important) |

**Run C** (exec_20260320_073836_6nb7bk21):
| Metric | Value |
|---|---|
| Duration | 2097s (~35 min) |
| Dimensions | 9 |
| Total findings | 6 (1 critical, 5 important) |

**Combined GT coverage (best of both runs):**

| GT Bug | Run A | Run C | Best |
|---|---|---|---|
| GT-1 | NO | NO | NO |
| GT-2 | NO | NO | NO |
| GT-3 | NO | NO | NO |
| GT-4 | NO | NO | NO |
| GT-5 | NO | NO | NO |
| GT-6 | NO | NO | NO |
| GT-7 | NO | NO | NO |
| GT-8 | YES (5 findings) | YES (5 findings) | YES |
| GT-9 | NO | NO | NO |

**Recall: 1/9 (11%)**. All findings cluster around exception handling (GT-8). No other GT bug category detected.

**Finding-level analysis**: Both runs found real issues — the exception hierarchy problem, `contextlib.suppress(ValueError)` silently masking hex conversion failures, and generic `except Exception` handlers swallowing typed exceptions. These are genuine findings within GT-8, but they never break out of the exception handling cluster to detect type mismatches (GT-2, GT-4), cross-file contracts (GT-1, GT-3), or mechanical issues (GT-5, GT-6, GT-7).

### 2.5 Post-Redesign: PR-AF v3 + Architecture Redesign + minimax-m2.5 (Mar 20)

| Metric | Value |
|---|---|
| Architecture | v3 + research brief + cross-cluster patches + gap finder |
| Duration | 3935s (~66 min), budget exhausted at 162% |
| Dimensions | 15 (semantic=5, mechanical=5→2 dedup, systemic=5) → 12 launched |
| Total findings (raw) | 12 |
| Total findings (post-synthesis) | 3 (1 high, 2 medium) |
| Research brief | Returned empty (0 danger zones, 0 directives) |
| Gap finder | Not triggered (no research brief data) |
| Adversary | Skipped (budget exhausted) |

| GT Bug | Found? | Details |
|---|---|---|
| GT-1 | NO | Not detected |
| GT-2 | NO | Not detected |
| GT-3 | PARTIAL | Finding #1: "Missing key existence check in from_previous method" touches the `pbkdf2iters` area but doesn't identify the inconsistency |
| GT-4 | NO | Not detected |
| GT-5 | NO | Not detected |
| GT-6 | NO | Not detected |
| GT-7 | NO | Not detected |
| GT-8 | PARTIAL | Finding #2: "change_key requires loaded key but doesn't verify" is related but doesn't identify the core exception hierarchy issue |
| GT-9 | NO | Not detected |

**Recall: 0.5-1/9 (~11%)**. The 3 surviving findings are weaker than the pre-redesign findings (lower scores, less specific to GT bugs). Architecture improvements ran (15 dimensions vs 6-9), but the research brief returning empty nullified Improvements 1, 3, and 4.

---

## 3. Cross-System Coverage Matrix

| GT Bug | Sonnet v2 | Kimi v2 | Claude Code | minimax Pre | minimax Post |
|---|---|---|---|---|---|
| GT-1: Decorator dispatch | Investigated, ruled out | NO | **YES** | NO | NO |
| GT-2: Enum comparison | **YES** | NO | **YES** | NO | NO |
| GT-3: pbkdf2iters inconsistency | **YES** | **YES** | **YES** | NO | Partial |
| GT-4: KMIP cache wipe | **YES** | NO | **YES** | NO | NO |
| GT-5: Method shadowing | NO | **YES** | NO | NO | NO |
| GT-6: Duplicate export | NO | **YES** | NO | NO | NO |
| GT-7: Missing ds['id'] | **YES** | NO | NO | NO | NO |
| GT-8: Exception contract | **YES** | **YES** | NO | **YES** | Partial |
| GT-9: TOCTOU race | NO | **YES** | NO | NO | NO |
| **Recall** | **6/9 (67%)** | **6/9 (67%)** | **4/9 (44%)** | **1/9 (11%)** | **~1/9 (11%)** |

---

## 4. Scoring Rubric (Same as Mar 10 EVALUATION.md)

### 4.1 Recall (30% weight)

| System | Bugs Found | Score |
|---|---|---|
| Sonnet v2 (Mar 10) | 6/9 | 0.67 |
| Kimi v2 (Mar 10) | 6/9 | 0.67 |
| Claude Code (Mar 10) | 4/9 | 0.44 |
| minimax Pre-redesign | 1/9 | 0.11 |
| minimax Post-redesign | ~1/9 | 0.11 |

### 4.2 Precision (25% weight)

| System | True Positives / Total | Score |
|---|---|---|
| Sonnet v2 | ~14/14 | 0.96 |
| Kimi v2 | ~18-21/25 | 0.78 |
| Claude Code | ~5-6/6 | 0.92 |
| minimax Pre-redesign (Run A) | 5/5 (all real, but same cluster) | 0.85 |
| minimax Post-redesign | 2/3 (Finding #3 is defensive, not a real bug) | 0.67 |

### 4.3 Evidence Quality (20% weight)

| System | Score | Notes |
|---|---|---|
| Sonnet v2 | 0.87 | Consistently high, type-level analysis, traces execution paths |
| Kimi v2 | 0.68 | High for top findings, drops off significantly |
| Claude Code | 0.62 | Sufficient for identification, not remediation |
| minimax Pre-redesign | 0.65 | Good evidence for exception findings, includes code snippets and step-by-step traces |
| minimax Post-redesign | 0.40 | Shallow — findings lack code evidence, step-by-step traces, or specific line analysis |

### 4.4 Severity Calibration (15% weight)

| System | Score | Notes |
|---|---|---|
| Sonnet v2 | 0.92 | 2 critical, both genuinely critical |
| Kimi v2 | 0.70 | 6 critical, 4 adversary-challenged |
| Claude Code | 0.80 | 2 critical, CC-1 disputed |
| minimax Pre-redesign | 0.75 | 2 critical for hex-masking and exception hierarchy — reasonable for scope |
| minimax Post-redesign | 0.50 | "high" and "medium" labels don't match standard severity scale, lower confidence |

### 4.5 Breadth (10% weight)

| System | Dimensions | Score |
|---|---|---|
| Sonnet v2 | 6 distinct risk areas | 0.75 |
| Kimi v2 | 8 distinct risk areas | 0.90 |
| Claude Code | 3-4 risk areas | 0.50 |
| minimax Pre-redesign | 1 risk area (exception handling only) | 0.15 |
| minimax Post-redesign | 3 risk areas (exception handling, key format, code quality) | 0.30 |

### 4.6 Weighted Final Scores

| Criterion | Weight | Sonnet v2 | Kimi v2 | Claude Code | minimax Pre | minimax Post |
|---|---|---|---|---|---|---|
| Recall | 30% | 0.67 | 0.67 | 0.44 | 0.11 | 0.11 |
| Precision | 25% | 0.96 | 0.78 | 0.92 | 0.85 | 0.67 |
| Evidence quality | 20% | 0.87 | 0.68 | 0.62 | 0.65 | 0.40 |
| Severity calibration | 15% | 0.92 | 0.70 | 0.80 | 0.75 | 0.50 |
| Breadth | 10% | 0.75 | 0.90 | 0.50 | 0.15 | 0.30 |
| **Weighted total** | 100% | **0.828** | **0.727** | **0.656** | **0.448** | **0.356** |

**Calculation for minimax Pre**: (0.11×0.30)+(0.85×0.25)+(0.65×0.20)+(0.75×0.15)+(0.15×0.10) = 0.033+0.213+0.130+0.113+0.015 = **0.448**

**Calculation for minimax Post**: (0.11×0.30)+(0.67×0.25)+(0.40×0.20)+(0.50×0.15)+(0.30×0.10) = 0.033+0.168+0.080+0.075+0.030 = **0.356**

---

## 5. Architecture Redesign Impact Analysis

### 5.1 What the Architecture Redesign Changed

| Improvement | Implemented? | Activated? | Impact |
|---|---|---|---|
| **Imp 1: Research Brief** (Phase 2.5) | YES | YES — but returned empty | minimax-m2.5 failed to populate `ResearchBrief` schema. 0 danger zones, 0 directives. |
| **Imp 2: Cross-Cluster Patches** | YES | YES — programmatic, always works | Scouts received actual diff hunks from dependent files. No new GT bugs found despite this. |
| **Imp 3: Gap Finder** | YES | NO — requires research brief | Gap finder checks `research_brief_result` before running. Empty brief = no trigger. |
| **Imp 4: Coverage Requirements** | YES | NO — requires research brief | Strategist and coverage gate received no danger zones to enforce. |

### 5.2 Why the Architecture Didn't Improve Recall

The architecture redesign is **structurally correct but model-bottlenecked**:

1. **Research brief is the keystone**: Improvements 1, 3, and 4 all depend on the research brief producing danger zones. minimax-m2.5 returned an empty schema, collapsing 3 of 4 improvements.

2. **Cross-cluster patches (Imp 2) worked but weren't enough alone**: Scouts received diff hunks from dependent files, but minimax-m2.5 didn't use this context to discover cross-file bugs. The context was there; the model didn't reason deeply enough over it.

3. **More dimensions ≠ more recall**: The post-redesign run generated 15 dimensions (vs 6-9 pre-redesign) and found 12 raw findings (vs 5-6). But the additional findings were lower quality and clustered in the same areas. The synthesis phase correctly filtered to 3.

4. **Longer runtime, worse results**: 66 min vs 35-41 min, with fewer and weaker findings. The extra Phase 2.5 added ~5 min of wall time for zero payoff with this model.

### 5.3 Model Capability Gap

The core issue is the 6x recall gap between Sonnet 4.6 (67%) and minimax-m2.5 (11%):

| Capability | Sonnet 4.6 | minimax-m2.5 |
|---|---|---|
| Populate structured schemas | YES (ResearchBrief would work) | NO (returns empty) |
| Type-level runtime reasoning | YES (found GT-2, GT-4) | NO (never detects type mismatches) |
| Cross-file contract tracing | YES (found GT-7) | NO (even with cross-cluster patches) |
| Deep code analysis per finding | YES (traces full execution paths) | LIMITED (surface-level descriptions) |
| Schema compliance | YES (correct severity labels) | PARTIAL (non-standard labels like "high"/"medium") |

### 5.4 Projected Impact with Sonnet 4.6

Based on the structural improvements and Sonnet's demonstrated capabilities:

| Improvement | Expected impact with Sonnet |
|---|---|
| Research brief | Would produce danger zones for type mismatches, contract violations → scouts investigate with focus |
| Cross-cluster patches | Sonnet already found GT-7 (cross-file). Actual diff hunks could surface GT-1 (decorator), GT-5 (shadowing) |
| Gap finder | With populated danger zones, would investigate GT bugs that scouts missed |
| Coverage requirements | Would flag uncovered danger zones in coverage gate, triggering gap-fill dimensions |

**Projected Sonnet recall with architecture redesign: 7-8/9** (from 6/9 baseline), potentially catching GT-1 or GT-5 through cross-cluster context, and GT-9 through gap finder directives.

---

## 6. Conclusions

### 6.1 Current State

| Rank | System | Weighted Score | Recall | Best For |
|---|---|---|---|---|
| 1 | PR-AF v2 + Sonnet 4.6 | **0.828** | 6/9 (67%) | Precision-critical deep review |
| 2 | PR-AF v2 + Kimi k2.5 | **0.727** | 6/9 (67%) | Broad coverage, complementary bugs |
| 3 | Claude Code | **0.656** | 4/9 (44%) | Speed, GitHub-native, unique GT-1 |
| 4 | PR-AF v3 + minimax Pre | **0.448** | 1/9 (11%) | Cost-efficient exception analysis |
| 5 | PR-AF v3 + minimax Post | **0.356** | 1/9 (11%) | Architecture validated, model bottleneck |

### 6.2 Key Findings

1. **Model choice dominates architecture choice.** The 6x recall gap between Sonnet and minimax dwarfs any architectural improvement. Architecture enables, but model capability delivers.

2. **The architecture redesign is structurally sound but untested with a capable model.** All 4 improvements deployed correctly. Phase 2.5 ran, cross-cluster patches flowed, 15 dimensions generated. The bottleneck is minimax-m2.5's inability to populate structured outputs and reason about runtime types.

3. **minimax-m2.5 is a one-trick pony on this PR.** Across 4 runs (3 pre, 1 post), it only ever finds exception handling issues (GT-8). It never discovers type mismatches, cross-file contracts, name shadowing, missing arguments, or concurrency bugs — regardless of architecture.

4. **The v3 efficiency features (adaptive gates, multi-tier routing) did not improve recall.** The efficiency branch optimizations (from the `feat/precision-and-efficiency` commit) are about cost/speed, not recall. Recall requires model capability + architecture that provides the right context.

5. **No single system catches everything.** The combined coverage of all 5 systems is 9/9 (100%). Even Sonnet+Kimi combined only reach 8/9. GT-1 remains a Claude Code exclusive.

### 6.3 Recommendations

1. **Run the architecture redesign with Sonnet 4.6** to measure the actual recall improvement. The architecture is designed for models that can populate `ResearchBrief` and reason about danger zones — Sonnet can do this.

2. **Consider making the research brief fallback to a simpler schema** when the primary schema returns empty. Instead of a full `ResearchBrief`, fall back to a narrative string output from the harness that downstream phases parse as context.

3. **minimax-m2.5 should not be the benchmark model.** It's suitable for cost-efficient reviews where exception handling coverage is sufficient, but it cannot validate architecture improvements designed for deep reasoning.

4. **Track the 12 raw findings (pre-synthesis)** from the post-redesign run. The synthesis phase reduced 12→3, which may have been too aggressive. Some of those 12 may have partially covered additional GT bugs.

---

## 7. Data Sources

| Run | Execution ID | File |
|---|---|---|
| Pre-redesign Run A | exec_20260320_073251_dwhg6a3q | `runs/exec_20260320_073251_dwhg6a3q.json` |
| Pre-redesign Run B | exec_20260320_073747_16jagaml | `runs/exec_20260320_073747_16jagaml.json` (0 findings, m2.7) |
| Pre-redesign Run C | exec_20260320_073836_6nb7bk21 | `runs/exec_20260320_073836_6nb7bk21.json` |
| Post-redesign | exec_20260320_091457_c0aj1ozi | `runs/exec_20260320_091457_c0aj1ozi.json` |
| Historical Sonnet | — | `pr-af-result-sonnet.json` |
| Historical Kimi | — | `pr-af-result-kimi.json` |
| Historical Claude Code | — | `claude-code-inline-comments.json` |

---

*Evaluation produced 2026-03-20. Methodology matches EVALUATION.md (2026-03-10) for comparability. All findings sourced from execution result JSONs in this directory.*
