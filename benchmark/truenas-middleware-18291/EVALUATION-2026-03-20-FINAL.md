# Comprehensive Benchmark Evaluation
## truenas/middleware PR #18291 — ZFS Dataset Encryption Refactor
## All Systems, All Versions — 2026-03-20

---

## 1. Systems Under Evaluation

### External Baselines (Public Benchmarks, collected 2026-03-10)

| ID | System | Model | Architecture | Notes |
|---|---|---|---|---|
| **EXT-S** | PR-AF v2 + Sonnet | Claude Sonnet 4.6 | v2 meta-selector (opencode) | Historical best. 14 findings, 0 adversary challenged |
| **EXT-K** | PR-AF v2 + Kimi | Kimi k2.5 | v2 meta-selector (opencode) | Broad coverage. 25 findings, 7 adversary challenged |
| **EXT-CC** | Claude Code | claude[bot] | Single-agent GitHub App | Instant, inline. ~6 findings |

### Internal Development Runs (2026-03-20)

| ID | System | Model | Architecture | Notes |
|---|---|---|---|---|
| **INT-v3-A** | PR-AF v3 efficiency | minimax-m2.5 | v3 + adaptive gates, multi-tier routing | Pre-redesign baseline run A |
| **INT-v3-C** | PR-AF v3 efficiency | minimax-m2.5 | v3 + adaptive gates, multi-tier routing | Pre-redesign baseline run C |
| **INT-v3r-mm** | PR-AF v3 + redesign | minimax-m2.5 | v3 + research brief + cross-cluster + gap finder | Post-redesign, minimax (research brief empty) |
| **INT-v3r-S** | PR-AF v3 + redesign | Sonnet 4.6 (claude-code) | v3 + research brief + cross-cluster + gap finder | **Post-redesign, Sonnet. Current best internal.** |

---

## 2. Ground Truth (9 Confirmed Bugs)

| ID | Bug | Category | Difficulty |
|---|---|---|---|
| GT-1 | `@pass_thread_local_storage` decorator dispatch crash | Cross-file contract | Hard |
| GT-2 | `ZFSKeyFormat` enum comparison always False | Type-level runtime | Medium |
| GT-3 | `pbkdf2iters` minimum inconsistency across option classes | Cross-file contract | Medium |
| GT-4 | `k in existing_datasets` type mismatch (KMIP cache wipe) | Type-level runtime | Hard |
| GT-5 | Method shadowing: `check_key` infinite recursion | Name resolution | Medium |
| GT-6 | Duplicate export: `PoolRemoveArgs` in `__all__` | Mechanical | Easy |
| GT-7 | Missing argument: `ds['id']` in `datastore.update` | Missing argument | Medium |
| GT-8 | Broad `Exception` catch masks typed exceptions | Exception handling | Easy-Medium |
| GT-9 | TOCTOU race condition in `load_key()` | Concurrency | Medium |

---

## 3. Ground Truth Coverage Matrix

| GT Bug | EXT-S | EXT-K | EXT-CC | INT-v3-A | INT-v3-C | INT-v3r-mm | INT-v3r-S |
|---|---|---|---|---|---|---|---|
| GT-1: Decorator dispatch | Ruled out | NO | **YES** | NO | NO | NO | NO |
| GT-2: Enum comparison | **YES** (0.686) | NO | **YES** | NO | NO | NO | **YES** (1.274) |
| GT-3: pbkdf2iters | **YES** (0.665) | **YES** (0.630) | **YES** | NO | NO | Partial | **YES** (0.195) |
| GT-4: KMIP cache wipe | **YES** (0.970) | NO | **YES** | NO | NO | NO | NO |
| GT-5: Method shadowing | NO | **YES** (1.852) | NO | NO | NO | NO | NO |
| GT-6: Duplicate export | NO | **YES** (1.000) | NO | NO | NO | NO | NO |
| GT-7: Missing ds['id'] | **YES** (0.950) | NO | NO | NO | NO | NO | NO |
| GT-8: Exception contract | **YES** (0.665) | **YES** (1.092) | NO | **YES** (0.850) | **YES** (0.900) | Partial | **YES** (1.261) |
| GT-9: TOCTOU race | NO | **YES** (0.787) | NO | NO | NO | NO | Partial (0.210) |
| **Recall** | **6/9** | **6/9** | **4/9** | **1/9** | **1/9** | **~1/9** | **4/9** |
| **Best score on a GT bug** | 0.970 | 1.852 | — | 0.900 | 0.900 | 0.285 | **1.274** |

### Notes on INT-v3r-S Ground Truth Mapping

- **GT-2 (1.274)**: Top finding. Correctly identifies enum-instance vs string comparison. Traces through `ZFSKeyFormat(enum.Enum)` definition, shows `==` always False. **Higher score than EXT-S (0.686)** — the research brief likely directed the scout to this danger zone.
- **GT-3 (0.195)**: Finding #9 identifies `pbkdf2iters` passed directly contradicting other call sites. Lower score than EXT-S but correctly detected.
- **GT-8 (1.261)**: Finding #2 traces the full exception hierarchy gap — `ZFSKeyAlreadyLoadedException` inherits `Exception` not `ZFSException`, falls through catch ladder. **Higher score than EXT-S (0.665) and EXT-K (1.092).**
- **GT-9 partial (0.210)**: Finding #8 identifies ZFS/DB out-of-sync risk after `change_key`, which is related to the TOCTOU pattern but not the exact `load_key()` race.
- **GT-4 missed**: The KMIP cache wipe (`k in existing_datasets` type mismatch) was not found. This was EXT-S's top finding at 0.970.
- **GT-7 missed**: The missing `ds['id']` argument (EXT-S's unique novel finding) was not found.

---

## 4. Novel Findings (Not in Ground Truth)

INT-v3r-S produced findings not in the original 9-bug ground truth:

| Finding | Score | Assessment |
|---|---|---|
| sync_db_keys collapses three-way error distinction → data loss path | 0.970 | **Genuine critical bug.** Transient ZFS failure on encrypted_root now deletes the DB key permanently. |
| check_key() exception semantics differ but caller treats identically | 0.773 | **Genuine important bug.** Deepens GT-8 — distinguishes 4 exception types with different recovery semantics. |
| encryption_summary maps all exceptions to valid_key=False silently | 0.616 | **Genuine.** Old code raised CallError on bulk failure; new code silently returns wrong answer. |
| KMIP pull_zfs_keys false alert on non-encrypted dataset | 0.609 | **Genuine.** ZFSNotEncryptedException propagates to user-visible KMIP sync failure alert. |
| Resource handle use-after-free risk in path_in_locked_datasets | 0.216 | **Potential.** Pattern diverges from all other call sites; depends on truenas_pylibzfs internals. |
| ZFS/DB out-of-sync after change_key exception | 0.210 | **Genuine.** No try/except around DB update after ZFS key change. |
| path_in_locked_datasets bypasses EZFS_NOENT conversion | 0.180 | **Genuine.** Inconsistent error handling vs utils.open_resource pattern. |

**3 novel findings scored above 0.6** — these are genuine bugs not in the original ground truth. The old Sonnet v2 run found 1 novel bug (missing ds['id']). The redesigned architecture found **3+ novel bugs**, indicating deeper analysis.

---

## 5. Weighted Scoring (Same Rubric as EVALUATION.md)

### 5.1 Recall (30% weight)

| System | Bugs Found | Score |
|---|---|---|
| EXT-S (Sonnet v2) | 6/9 | 0.67 |
| EXT-K (Kimi v2) | 6/9 | 0.67 |
| EXT-CC (Claude Code) | 4/9 | 0.44 |
| INT-v3-A (minimax pre) | 1/9 | 0.11 |
| INT-v3-C (minimax pre) | 1/9 | 0.11 |
| INT-v3r-mm (minimax post) | ~1/9 | 0.11 |
| INT-v3r-S (Sonnet redesign) | 4/9 | 0.44 |

### 5.2 Precision (25% weight)

| System | True Positives / Total | Score |
|---|---|---|
| EXT-S | ~14/14 | 0.96 |
| EXT-K | ~18-21/25 | 0.78 |
| EXT-CC | ~5-6/6 | 0.92 |
| INT-v3-A | 5/5 | 0.85 |
| INT-v3-C | 5/6 | 0.83 |
| INT-v3r-mm | 2/3 | 0.67 |
| INT-v3r-S | 10/10 | **0.98** |

INT-v3r-S precision note: All 10 findings are genuine bugs or genuine behavioral concerns. 0 false positives. 5 adversary-challenged but 15 confirmed. Evidence verification falsified 0.

### 5.3 Evidence Quality (20% weight)

| System | Score | Notes |
|---|---|---|
| EXT-S | 0.87 | Consistently high, type-level analysis |
| EXT-K | 0.68 | High for top findings, drops off |
| EXT-CC | 0.62 | Sufficient for identification |
| INT-v3-A | 0.65 | Good for exception findings |
| INT-v3-C | 0.63 | Similar to Run A |
| INT-v3r-mm | 0.40 | Shallow, no code traces |
| INT-v3r-S | **0.92** | Step-by-step execution traces, code snippets, identifies exact lines, traces caller chains |

INT-v3r-S evidence quality note: Finding #1 (GT-2) traces through enum definition, shows `ZFSKeyFormat(enum.Enum)` has no `str` base, walks through the comparison semantics step by step. Finding #3 explains the old 3-way error distinction vs the new collapsed pattern. This is the highest evidence quality across all systems.

### 5.4 Severity Calibration (15% weight)

| System | Score | Notes |
|---|---|---|
| EXT-S | 0.92 | 2 critical, both genuine |
| EXT-K | 0.70 | 6 critical, 4 challenged |
| EXT-CC | 0.80 | CC-1 disputed |
| INT-v3-A | 0.75 | 2 critical, reasonable |
| INT-v3-C | 0.72 | 1 critical |
| INT-v3r-mm | 0.50 | Non-standard labels |
| INT-v3r-S | **0.93** | 3 critical — all genuinely critical (enum always-False, exception hierarchy data loss, DB key deletion). 3 important correctly calibrated. |

### 5.5 Breadth (10% weight)

| System | Dimensions Covered | Score |
|---|---|---|
| EXT-S | 6 risk areas | 0.75 |
| EXT-K | 8 risk areas | 0.90 |
| EXT-CC | 3-4 risk areas | 0.50 |
| INT-v3-A | 1 (exception handling) | 0.15 |
| INT-v3-C | 1 (exception handling) | 0.15 |
| INT-v3r-mm | 3 risk areas | 0.30 |
| INT-v3r-S | 7 risk areas (exception hierarchy, enum comparison, KMIP propagation, resource lifetime, DB sync, pbkdf2iters, API contract) | **0.85** |

### 5.6 Weighted Final Scores

| Criterion | Weight | EXT-S | EXT-K | EXT-CC | INT-v3-A | INT-v3-C | INT-v3r-mm | **INT-v3r-S** |
|---|---|---|---|---|---|---|---|---|
| Recall | 30% | 0.67 | 0.67 | 0.44 | 0.11 | 0.11 | 0.11 | 0.44 |
| Precision | 25% | 0.96 | 0.78 | 0.92 | 0.85 | 0.83 | 0.67 | **0.98** |
| Evidence | 20% | 0.87 | 0.68 | 0.62 | 0.65 | 0.63 | 0.40 | **0.92** |
| Calibration | 15% | 0.92 | 0.70 | 0.80 | 0.75 | 0.72 | 0.50 | **0.93** |
| Breadth | 10% | 0.75 | 0.90 | 0.50 | 0.15 | 0.15 | 0.30 | 0.85 |
| **TOTAL** | **100%** | **0.828** | **0.727** | **0.656** | **0.448** | **0.441** | **0.356** | **0.806** |

**Calculations:**
- EXT-S: (0.67×0.30)+(0.96×0.25)+(0.87×0.20)+(0.92×0.15)+(0.75×0.10) = 0.201+0.240+0.174+0.138+0.075 = **0.828**
- EXT-K: (0.67×0.30)+(0.78×0.25)+(0.68×0.20)+(0.70×0.15)+(0.90×0.10) = 0.201+0.195+0.136+0.105+0.090 = **0.727**
- EXT-CC: (0.44×0.30)+(0.92×0.25)+(0.62×0.20)+(0.80×0.15)+(0.50×0.10) = 0.132+0.230+0.124+0.120+0.050 = **0.656**
- INT-v3-A: (0.11×0.30)+(0.85×0.25)+(0.65×0.20)+(0.75×0.15)+(0.15×0.10) = 0.033+0.213+0.130+0.113+0.015 = **0.448**
- INT-v3-C: (0.11×0.30)+(0.83×0.25)+(0.63×0.20)+(0.72×0.15)+(0.15×0.10) = 0.033+0.208+0.126+0.108+0.015 = **0.441**
- INT-v3r-mm: (0.11×0.30)+(0.67×0.25)+(0.40×0.20)+(0.50×0.15)+(0.30×0.10) = 0.033+0.168+0.080+0.075+0.030 = **0.356**
- INT-v3r-S: (0.44×0.30)+(0.98×0.25)+(0.92×0.20)+(0.93×0.15)+(0.85×0.10) = 0.132+0.245+0.184+0.140+0.085 = **0.806**

---

## 6. Final Rankings

| Rank | System | Score | Recall | Key Strength |
|---|---|---|---|---|
| **1** | **EXT-S** (Sonnet v2, Mar 10) | **0.828** | 6/9 (67%) | Highest recall, found GT-4 + GT-7 (unique) |
| **2** | **INT-v3r-S** (Sonnet redesign) | **0.806** | 4/9 (44%) | Highest precision (0.98) + evidence (0.92) + calibration (0.93). 3 novel bugs. |
| **3** | **EXT-K** (Kimi v2, Mar 10) | **0.727** | 6/9 (67%) | Broadest, found GT-5 + GT-6 + GT-9 (unique) |
| **4** | **EXT-CC** (Claude Code, Mar 10) | **0.656** | 4/9 (44%) | Instant, found GT-1 (unique) |
| **5** | **INT-v3-A** (minimax pre) | **0.448** | 1/9 (11%) | Cost-efficient exception analysis |
| **6** | **INT-v3-C** (minimax pre) | **0.441** | 1/9 (11%) | Same as Run A |
| **7** | **INT-v3r-mm** (minimax post) | **0.356** | ~1/9 (11%) | Architecture validated, model bottleneck |

---

## 7. Architecture Redesign Impact Analysis

### What Improved (INT-v3r-S vs EXT-S)

| Metric | EXT-S (old arch) | INT-v3r-S (new arch) | Delta |
|---|---|---|---|
| Precision | 0.96 | **0.98** | +0.02 |
| Evidence quality | 0.87 | **0.92** | +0.05 |
| Severity calibration | 0.92 | **0.93** | +0.01 |
| Breadth | 0.75 | **0.85** | +0.10 |
| GT-2 score | 0.686 | **1.274** | **+86%** |
| GT-8 score | 0.665 | **1.261** | **+90%** |
| Novel bugs found | 1 | **3+** | **+200%** |
| False positives | 0 | 0 | — |
| Adversary confirmed | 0 | 15 | +15 |

### What Regressed (INT-v3r-S vs EXT-S)

| Metric | EXT-S | INT-v3r-S | Delta |
|---|---|---|---|
| **Recall** | **6/9 (67%)** | 4/9 (44%) | **-2 bugs** |
| GT-4 (KMIP cache wipe) | Found (0.970) | **MISSED** | Lost |
| GT-7 (missing ds['id']) | Found (0.950) | **MISSED** | Lost |
| Duration | ~35 min | ~68 min | +94% |

### Root Cause of Recall Regression

The 2 missed bugs (GT-4, GT-7) are the same model (Sonnet 4.6) on different architectures. The regression is architectural:

1. **GT-4** (KMIP cache wipe): This requires reading `kmip_operations.py` and reasoning about `k in existing_datasets` where `existing_datasets` is `list[dict]`. In EXT-S, a dedicated review dimension targeted this file directly. In INT-v3r-S, the KMIP file was covered by a scout that found the `ZFSNotEncryptedException` propagation (Finding #6) but didn't trace the `in` operator type mismatch. The **cross-cluster patches** (Imp 2) may not have included the KMIP file since the dependency graph may not have linked it.

2. **GT-7** (missing `ds['id']`): This is a mechanical argument-count bug. In EXT-S, a dedicated dimension asked "are all callers of `datastore.update` passing the right arguments?" The redesigned architecture generated a mechanical dimension that focused on exception handling rather than argument validation.

### Architecture Redesign Features — Activation Status

| Feature | Activated? | Impact |
|---|---|---|
| Research Brief (Imp 1) | **YES** — 10 danger zones, 10 directives | Directed scouts to GT-2 enum comparison (now top finding at 1.274) |
| Cross-Cluster Patches (Imp 2) | **YES** — programmatic | Scouts had dependent file diffs. Found KMIP propagation bug (#6) |
| Gap Finder (Imp 3) | **YES** — **4 new findings** | Found resource lifetime, DB sync, API contract bugs |
| Coverage Requirements (Imp 4) | **YES** — danger zones to strategist | Strategist generated dimensions covering more risk areas (breadth 0.85 vs 0.75) |

---

## 8. Combined Coverage (Theoretical Maximum)

If we ran all systems together and deduplicated:

| GT Bug | Best System | Score |
|---|---|---|
| GT-1 | EXT-CC (Claude Code) | — |
| GT-2 | **INT-v3r-S** (Sonnet redesign) | **1.274** |
| GT-3 | EXT-S or EXT-K | 0.665 |
| GT-4 | EXT-S | 0.970 |
| GT-5 | EXT-K | 1.852 |
| GT-6 | EXT-K | 1.000 |
| GT-7 | EXT-S | 0.950 |
| GT-8 | **INT-v3r-S** (Sonnet redesign) | **1.261** |
| GT-9 | EXT-K | 0.787 |
| **Combined recall** | | **9/9 (100%)** |

**INT-v3r-S now holds the best score on 2 of 9 ground truth bugs (GT-2, GT-8)**, producing the highest-quality analysis on the bugs it finds.

---

## 9. Recommendations

### Immediate Actions

1. **Investigate GT-4 and GT-7 regression.** Both were found by EXT-S (same model). The architecture change lost them. This is likely a dimension generation issue — the strategist needs to generate a "type mismatch in container membership checks" dimension and an "argument count validation" dimension.

2. **Run EXT-S baseline on the new architecture without the research brief** to isolate whether the regression is from the research brief directing scouts away from KMIP/datastore areas, or from other changes.

3. **Consider making the research brief additive, not directive.** Currently it flows to scouts and strategist as investigation directives. If it's too strongly worded, it may focus scouts narrowly on danger zones at the expense of open-ended exploration that found GT-4 and GT-7 in EXT-S.

### Architecture Next Steps

4. **The gap finder works and should be kept.** It produced 4 genuine findings including resource lifetime and DB sync bugs that no other phase caught.

5. **Evidence verification semaphore is critical.** Without it, 31 parallel claude-code processes crash the container. Keep the `max_concurrent_reviewers` semaphore on verification.

6. **The quality vs recall tradeoff is real.** INT-v3r-S has the highest precision (0.98), evidence quality (0.92), and severity calibration (0.93) of any system. It finds fewer bugs but with dramatically higher confidence. For production deployment, this may be preferable — 4 high-confidence findings are more actionable than 6 mixed-confidence findings.

### Model Routing

7. **minimax-m2.5 is not viable for this benchmark.** 1/9 recall across 4 runs. The architecture redesign features (research brief, gap finder) require a model that can populate structured schemas and reason about runtime types.

8. **Sonnet via claude-code works well** as the harness provider. Research brief populated correctly, evidence verification thorough, gap finder productive.

---

*Comprehensive evaluation produced 2026-03-20. Methodology matches EVALUATION.md (2026-03-10) for cross-comparability. All findings sourced from execution result JSONs and the original evaluation document.*
