# TrueNAS PR #18291 Benchmark Tracking

## Ground Truth (9 bugs)

| ID | Bug | Category |
|---|---|---|
| GT-1 | CC-1: `@pass_thread_local_storage` decorator dispatch crash | Cross-file contract |
| GT-2 | CC-2: `ZFSKeyFormat` enum comparison always False | Type-level runtime |
| GT-3 | CC-3: `pbkdf2iters` minimum inconsistency across option classes | Cross-file contract |
| GT-4 | CC-4: `k in existing_datasets` type mismatch (KMIP cache wipe) | Type-level runtime |
| GT-5 | Method shadowing: `check_key` causes infinite recursion | Name shadowing |
| GT-6 | Duplicate export: `PoolRemoveArgs` in `__all__` | Mechanical |
| GT-7 | Missing argument: `ds['id']` in `datastore.update` | Missing argument |
| GT-8 | Exception contract: broad `Exception` catch masks `ZFSNotEncryptedException` | Exception contract |
| GT-9 | TOCTOU race condition in `load_key()` | Concurrency |

---

## Run: 2026-03-20 — Pre-Architecture-Redesign Baseline (v3 efficiency branch)

**Architecture version**: feat/precision-and-efficiency (dynamic efficiency, adaptive gates, multi-tier routing)
**Model**: minimax/minimax-m2.5 via opencode
**3 parallel runs** on same PR, same model, same architecture.

### Run A: exec_20260320_073251_dwhg6a3q (minimax-m2.5)
- **Duration**: 2449s (~41 min), budget exhausted
- **Findings**: 5 (2 critical, 3 important)
- **Adversary**: 0 challenged, 0 confirmed

| Finding | Severity | Score | Maps to GT? |
|---|---|---|---|
| Silent hex conversion failure masks key material corruption | critical | 0.900 | Partial GT-8 (exception handling) |
| Custom exceptions inherit from base Exception | critical | 0.850 | GT-8 |
| ZFSKeyAlreadyLoadedException treated as failure | important | 0.560 | GT-8 (related) |
| Custom exceptions silently swallowed | important | 0.525 | GT-8 |
| No exception handling for change_key() | important | 0.490 | GT-8 (related) |

**Recall**: 1/9 (GT-8 only, found multiple times). Misses GT-1 through GT-7, GT-9.

### Run B: exec_20260320_073747_16jagaml (minimax-m2.7)
- **Duration**: 2131s (~36 min), budget exhausted
- **Findings**: 0
- **Note**: m2.7 model produced zero findings (permission error visible in logs)

**Recall**: 0/9

### Run C: exec_20260320_073836_6nb7bk21 (minimax-m2.5)
- **Duration**: 2097s (~35 min), budget exhausted
- **Findings**: 6 (1 critical, 5 important)

| Finding | Severity | Score | Maps to GT? |
|---|---|---|---|
| Silent failure when hex conversion fails | critical | 0.900 | Partial GT-8 |
| Masked error message - ValueError to 'Missing key' | important | 0.595 | GT-8 (related) |
| New custom exceptions not specifically caught | important | 0.595 | GT-8 |
| check_key exceptions caught by bare except | important | 0.560 | GT-8 |
| No defensive check for crypto.load_key existence | important | 0.525 | Not GT |
| check_key relies on EZFS_CRYPTOFAILED for wrong keys | important | 0.525 | Partial GT-8 |

**Recall**: 1/9 (GT-8 cluster only). Misses GT-1 through GT-7, GT-9.

### Baseline Summary

| Metric | Run A | Run B | Run C | Best of 3 |
|---|---|---|---|---|
| Recall (of 9 GT bugs) | 1/9 (11%) | 0/9 (0%) | 1/9 (11%) | 1/9 (11%) |
| Total findings | 5 | 0 | 6 | 6 |
| Critical findings | 2 | 0 | 1 | 2 |
| Duration (s) | 2449 | 2131 | 2097 | — |
| Adversary challenges | 0 | 0 | 0 | 0 |

**Analysis**: The current architecture with minimax-m2.5 is heavily clustered around exception handling (GT-8) and completely misses:
- Type-level runtime bugs (GT-2, GT-4) — no agent deeply reasons about runtime types
- Cross-file contract violations (GT-1, GT-5) — scouts don't see caller code in other clusters
- Missing arguments (GT-7) — no agent looks for mechanical correctness gaps
- Concurrency issues (GT-9) — TOCTOU not surfaced
- Duplicate exports (GT-6) — mechanical issue not detected

**Note**: The previous Sonnet 4.6 run (March 10, EVALUATION.md) achieved 6/9 recall. The current minimax-m2.5 runs show significant recall regression, likely due to model capability differences rather than architecture. The architecture redesign targets the structural gaps regardless of model choice.

---

## Historical Comparison (from EVALUATION.md, March 10)

| System | Model | Recall | Findings | Duration |
|---|---|---|---|---|
| PR-AF v2 | Sonnet 4.6 | 6/9 (67%) | 14 | ~35 min |
| PR-AF v2 | Kimi k2.5 | 6/9 (67%) | 25 | ~19 min |
| Claude Code | claude[bot] | 4/9 (44%) | ~6 | instant |
| **PR-AF v3 (current)** | **minimax-m2.5** | **1/9 (11%)** | **5-6** | **~35-41 min** |

---

## Next: Post-Architecture-Redesign (research brief + cross-cluster + gap finder)

Target: 3-5/9 recall with minimax-m2.5, 7-9/9 with Sonnet 4.6
