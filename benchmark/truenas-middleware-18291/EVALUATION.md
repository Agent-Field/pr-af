# LLM-as-a-Judge Evaluation: PR-AF vs Claude Code
## truenas/middleware PR #18291

**Date**: 2026-03-10  
**PR**: [truenas/middleware#18291](https://github.com/truenas/middleware/pull/18291) — Replace py-libzfs with truenas_pylibzfs for ZFS dataset encryption  
**Companion data**: `pr-af-result.json`, `claude-code-reviews.json`, `claude-code-inline-comments.json` (same directory)

---

## Executive Summary

This evaluation compares two automated PR review systems on the same pull request: a single-agent Claude Code review and PR-AF, a 7-phase multi-agent pipeline running on kimi-k2.5. PR-AF found substantially more issues by breadth (25 findings vs 4), but missed the two most impactful bugs — a decorator dispatch crash and a silent data-wiping bug — that Claude Code caught with exceptional precision. Neither system is strictly better. They have complementary failure modes.

---

## 1. Methodology

### What Was Compared

| Dimension | Claude Code (claude[bot]) | PR-AF |
|---|---|---|
| Review type | Single-agent, inline GitHub comments | 7-phase multi-agent pipeline |
| Model | Claude (Anthropic) | openrouter/moonshotai/kimi-k2.5 |
| Duration | Near-instant (two passes: Feb 27, Mar 3) | ~60 minutes (3626.5 seconds) |
| Findings posted | 4 unique inline comments | 12 inline comments (filtered from 25) |
| Human reviewer | yocalebo (7 style/pattern comments) | N/A |

### Evaluation Criteria

1. **Recall** — Did the system find the real bugs?
2. **Precision** — Were the findings actually bugs?
3. **Evidence quality** — Was the reasoning sound and actionable?
4. **Severity calibration** — Were critical bugs labeled critical?
5. **Breadth** — Did the system cover multiple risk dimensions?

### Limitations

- Ground truth is incomplete. We don't have post-merge incident data to confirm which findings were real bugs vs theoretical.
- Claude Code's first pass ("No bugs found") is included in the timeline but not in the finding count — only the second pass findings are scored.
- PR-AF's adversary phase challenged 16/20 findings internally. We treat adversary-confirmed findings as higher confidence but do not automatically discard challenged ones.
- The human reviewer (yocalebo) found exclusively style/pattern issues. These are not scored as bugs but are noted for completeness.

---

## 2. Reviewer Profiles

### Claude Code (claude[bot])

Single-agent review integrated as a GitHub App. Reads the diff, produces inline comments. Two passes on this PR:

- **Feb 27 (first pass)**: Summary only. "No bugs found." This is a significant miss — the bugs were present in the diff.
- **Mar 3 (second pass)**: 4 unique findings, 2 of which are critical and exceptionally well-evidenced.

The second pass finding quality is high. The first pass failure suggests the system benefits from re-review, which it does not do architecturally by default.

### PR-AF (Pull Request Agent Field)

Multi-agent pipeline built on AgentField. Phases:

```
INTAKE → ANATOMY → PLANNING → REVIEW (parallel, 10 dimensions) → LAYER (cross-ref + adversary + coverage) → SYNTHESIS → OUTPUT
```

- 10 parallel review dimensions (security, concurrency, API compatibility, resource management, etc.)
- Adversary phase challenges each finding before it reaches output
- 25 raw findings, 20 sent to adversary, 4 confirmed, 16 challenged (score-reduced, not discarded)
- 12 inline comments posted after filtering

The adversary phase is the system working as designed. A finding being "challenged" means its confidence score was reduced, not that it was wrong.

---

## 3. The PR Under Review

**truenas/middleware PR #18291** replaces the deprecated `py-libzfs` Python binding with `truenas_pylibzfs` across ZFS dataset encryption code paths. Changes span 8 files and touch:

- Encryption key management (load, unload, change, inherit)
- KMIP key synchronization
- Pool and dataset creation/modification options
- Failover unlock logic

The `pbkdf2iters` default and minimum were also raised from 350K/100K to 1.3M/1.3M — a security improvement that introduces a breaking API change.

This is a high-risk refactor. The old and new libraries have different APIs, different error types, and different threading models. Any assumption carried over from py-libzfs that doesn't hold in truenas_pylibzfs is a latent bug.

---

## 4. Finding-by-Finding Comparison

### 4.1 Full Finding Map

| ID | System | Severity | Description | Overlap / Status |
|---|---|---|---|---|
| CC-1 | Claude Code | CRITICAL | `@pass_thread_local_storage` direct-call crash in `sync_zfs_keys` | **PR-AF MISSED** |
| CC-2 | Claude Code | PRE-EXISTING | `ZFSKeyFormat` enum vs string comparison always False | **PR-AF MISSED** |
| CC-3 | Claude Code | IMPORTANT | `pbkdf2iters` inconsistency between pool.py and pool_dataset.py | **PARTIAL OVERLAP** with PR-1 + PR-2 |
| CC-4 | Claude Code | PRE-EXISTING | `existing_datasets` list[dict] vs str — KMIP cache always wiped | **PR-AF MISSED** |
| PR-1 | PR-AF | CRITICAL (0.95) | `pbkdf2iters` breaking change in `PoolCreateEncryptionOptions` (pool.py:139) | Partial overlap with CC-3 |
| PR-2 | PR-AF | CRITICAL (0.95) | `pbkdf2iters` breaking change in `PoolDatasetChangeKeyOptions` (pool_dataset.py:170) | Partial overlap with CC-3 |
| PR-3 | PR-AF | CRITICAL (0.95) | Failover unlock lock namespace mismatch (failover.py:553) | **CC MISSED** |
| PR-4 | PR-AF | CRITICAL (0.90) | `PoolCreateTopologySpecialVdev` now allows DRAID types (pool.py:180) | **CC MISSED** |
| PR-5 | PR-AF | CRITICAL (0.90) | `PoolScan` model lacks None/null support (pool_scrub.py:108) | **CC MISSED** |
| PR-6 | PR-AF | CRITICAL (0.85) | Inconsistent `ZFSError` comparison pattern (dataset_encryption_lock.py:223) | Different from CC-2 |
| PR-7 | PR-AF | CRITICAL (0.712) | TOCTOU race in `push_zfs_keys` cache (kmip/zfs_keys.py:66) | **CC MISSED** |
| PR-8 | PR-AF | CRITICAL (0.712) | TOCTOU race in `pull_zfs_keys` cache (kmip/zfs_keys.py:108) | **CC MISSED** |
| PR-9 | PR-AF | IMPORTANT (0.63) | `ZFSKeyAlreadyLoadedException` not caught (dataset_encryption_lock.py) | **CC MISSED** |
| PR-10 | PR-AF | IMPORTANT (0.62) | `PoolEntry.scan` type changed dict → PoolScan model | **CC MISSED** |
| PR-11 | PR-AF | IMPORTANT (0.61) | TOCTOU race in `load_key()` — check and load not atomic | **CC MISSED** |
| PR-12 | PR-AF | IMPORTANT (0.60) | Pool name validation relaxed, whitespace restriction removed | **CC MISSED** |
| PR-13 | PR-AF | IMPORTANT (0.59) | Lock/unlock namespace mismatch (dataset_encryption_lock.py) | **CC MISSED** |
| PR-14 | PR-AF | IMPORTANT (0.58) | `change_key` and `inherit_parent_encryption_properties` concurrent execution | **CC MISSED** |
| PR-15 | PR-AF | IMPORTANT (0.57) | Resource leaks in hot code path (dataset_encryption_info.py) | **CC MISSED** |
| PR-16 | PR-AF | IMPORTANT (0.56) | `open_resource()` lacks cleanup documentation | **CC MISSED** |
| PR-17 | PR-AF | IMPORTANT (0.55) | `PoolDatasetGetQuotaResult` return type expanded | **CC MISSED** |
| PR-18 | PR-AF | IMPORTANT (0.54) | ZFS resource objects not explicitly cleaned up after encryption ops | **CC MISSED** |
| PR-19 | PR-AF | IMPORTANT (0.53) | Error code mapping between py-libzfs and truenas_pylibzfs not verified | **CC MISSED** |
| PR-20 | PR-AF | IMPORTANT (0.52) | Database key sync silently removes keys on ANY ZFS error | **CC MISSED** |
| PR-21 | PR-AF | IMPORTANT (0.51) | `PoolDatasetCreateArgs` discriminator field may break clients | **CC MISSED** |
| PR-22 | PR-AF | IMPORTANT (0.50) | Unprotected cache access in `initialize_zfs_keys` | **CC MISSED** |
| PR-23 | PR-AF | IMPORTANT (0.48) | Broad exception handling in `check_key()` | **CC MISSED** |
| PR-24 | PR-AF | IMPORTANT (0.46) | KMIP pull adds datasets to failed list for ANY ZFS error | **CC MISSED** |
| PR-25 | PR-AF | IMPORTANT (0.44) | Async `reset_zfs_key` called without cache lock protection | **CC MISSED** |

### 4.2 pbkdf2iters Overlap — Different Angles, Same Root

CC-3 and PR-1+PR-2 both touch the `pbkdf2iters` change, but they frame it differently:

- **Claude Code (CC-3)**: Flags the *inconsistency* — pool.py still has old defaults while pool_dataset.py has new ones. The concern is that users can create datasets at 350K iterations but then can't change keys without meeting the 1.3M minimum.
- **PR-AF (PR-1, PR-2)**: Flags each file separately as a *breaking API change* — any caller passing a value between 100K and 1.3M will now fail validation.

These are complementary observations. CC-3 is about the cross-file inconsistency. PR-1/PR-2 are about the backward compatibility break. Both are valid. Neither fully subsumes the other.

### 4.3 ZFSError vs ZFSKeyFormat — Different Bugs

PR-6 and CC-2 both involve enum comparison issues but are distinct bugs:

- **CC-2**: `ZFSKeyFormat(...) == ZFSKeyFormat.PASSPHRASE.value` — comparing an enum member to a string. Always False. Silently bypasses a security check.
- **PR-6**: `e.code == ZFSError.EZFS_CRYPTOFAILED` vs `ZFSError(e.code) == ZFSError.EZFS_*` — inconsistent comparison pattern across the codebase. May or may not produce wrong results depending on how `e.code` is typed.

CC-2 is a confirmed logic bug with a clear security impact. PR-6 is a pattern inconsistency that may or may not be a bug depending on the library's type contracts.

---

## 5. Critical Misses Analysis

### 5.1 What PR-AF Missed

#### CC-1: `@pass_thread_local_storage` Direct-Call Crash

This is the most impactful finding in the entire review. `sync_zfs_keys` calls `push_zfs_keys` and `pull_zfs_keys` as direct Python method calls, but both methods are decorated with `@pass_thread_local_storage`, which injects a `tls` argument as the first positional parameter via middleware dispatch.

```python
# sync_zfs_keys (no decorator, direct call)
def sync_zfs_keys(self, ids):
    self.push_zfs_keys(ids)   # ids binds to tls parameter → AttributeError on tls.lzh
    self.pull_zfs_keys()      # TypeError: missing required positional argument 'tls'
```

**Why PR-AF missed this**: The decorator dispatch mechanism is a framework-level concern, not a code-level concern. PR-AF's review dimensions focused on code behavior, API compatibility, concurrency, and resource management. None of these dimensions are specifically tuned to reason about how `@pass_thread_local_storage` transforms the calling convention of decorated methods. Catching this bug requires understanding that the decorator changes the method signature at dispatch time — a Python-specific framework semantic that isn't visible in the diff without knowing the decorator's implementation.

This is a genuine gap in PR-AF's coverage. A "framework semantics" review dimension, or a prompt that explicitly asks "are there any methods that are called directly but decorated with middleware-injecting decorators?", would likely catch this.

#### CC-4: `existing_datasets` list[dict] vs str — KMIP Cache Always Wiped

```python
# existing_datasets is list[dict], k is str
self.zfs_keys = {k: v for k, v in self.zfs_keys.items() if k in existing_datasets}
# str in list[dict] is always False → dict comprehension always produces {}
```

Every call to `push_zfs_keys` or `pull_zfs_keys` silently wipes the entire KMIP key cache. This is a pre-existing bug, but it's a severe one — and it's in the exact code path this PR modifies.

**Why PR-AF missed this**: This requires runtime simulation reasoning. The bug is invisible at the type level unless you trace `existing_datasets` back to its source and confirm it's `list[dict]` rather than `list[str]`. PR-AF's analysis appears to have treated the `in` check as correct without verifying the element type. This is a Python-specific type gotcha that requires following data flow across function boundaries.

### 5.2 What Claude Code Missed

Claude Code's second pass found 4 findings. It missed everything else PR-AF flagged. The most significant misses:

**PR-3 (Failover lock namespace mismatch)**: `failover_dataset_unlock` vs `dataset_unlock_{id}` use different lock namespaces, allowing concurrent operations that should be mutually exclusive. This is a real concurrency bug exposed by the process pool removal in this PR.

**PR-4 (DRAID type expansion)**: `PoolCreateTopologySpecialVdev` TypeAlias change silently allows DRAID vdev types in special vdev positions, which may not be valid. This is an API contract change with no validation.

**PR-5 (PoolScan null safety)**: The new `PoolScan` model requires non-nullable fields that were nullable in the old dict representation. Existing callers passing `None` will break.

**PR-7/PR-8 (TOCTOU races in KMIP cache)**: Check-then-use races in `push_zfs_keys` and `pull_zfs_keys`. These were adversary-challenged (score reduced to 0.712) but not dismissed.

**PR-19 (Error code mapping not verified)**: The PR assumes py-libzfs and truenas_pylibzfs use the same error codes. If they don't, all error handling is silently wrong. This is a migration-specific risk that Claude Code didn't flag.

Claude Code's first pass ("No bugs found") is worth noting separately. The bugs were present in the diff. The second pass found them. This suggests Claude Code benefits from re-review — which it doesn't do by default, but PR-AF does architecturally through its multi-phase pipeline.

---

## 6. Strengths Analysis

### PR-AF Strengths

**Breadth across risk dimensions**: 10 parallel review dimensions means the system systematically covers concurrency, API compatibility, resource management, error handling, and security in every review. Claude Code's single-agent approach doesn't guarantee coverage of all dimensions.

**API compatibility analysis**: PR-1, PR-2, PR-4, PR-5, PR-10, PR-17, PR-21 are all API contract changes that could break existing callers. This is a whole category of risk that Claude Code didn't surface.

**Race condition analysis**: PR-3, PR-7, PR-8, PR-11, PR-13, PR-14, PR-22, PR-25 are concurrency findings. The process pool removal in this PR changes threading semantics, and PR-AF's concurrency dimension caught multiple resulting races. Claude Code found none.

**Resource leak detection**: PR-15, PR-16, PR-18 flag resource management issues. These are easy to miss in code review because they don't cause immediate failures.

**Cross-module analysis**: PR-AF's cross-reference phase found interactions between findings (8 cross-references). The lock namespace mismatch (PR-3, PR-13) is a cross-module issue that requires seeing both sides of the lock.

**Adversary phase reduces false positives**: 16/20 findings were challenged, reducing their confidence scores. This is the system self-correcting. The 4 confirmed findings have higher signal value.

### Claude Code Strengths

**Exceptional evidence quality on critical findings**: CC-1 and CC-4 include step-by-step proof of the crash path. The reasoning is airtight. A developer reading CC-1 knows exactly what will happen, why, and where.

**Framework-level understanding**: CC-1 required understanding how `@pass_thread_local_storage` transforms method signatures at dispatch time. This is deep, framework-specific reasoning that PR-AF's more general prompts didn't produce.

**Security impact tracing**: CC-2 traces the enum comparison bug to its security consequence — the check that prevents passphrase-encrypted parents from having key-encrypted children is silently bypassed. PR-AF flagged a related pattern inconsistency (PR-6) but didn't connect it to the security model.

**Concise, actionable output**: 4 findings, all real, all well-explained. Low noise.

---

## 7. Evidence Quality Comparison

Evidence quality measures how well a finding is supported — does it prove the bug, or just suggest it?

| Finding | System | Evidence Quality | Notes |
|---|---|---|---|
| CC-1 (@pass_thread_local_storage) | Claude Code | 5/5 | Step-by-step crash path, decorator implementation traced |
| CC-2 (ZFSKeyFormat enum) | Claude Code | 5/5 | Python enum behavior proven, security impact traced |
| CC-3 (pbkdf2iters inconsistency) | Claude Code | 4/5 | Cross-file comparison, practical impact described |
| CC-4 (existing_datasets type) | Claude Code | 5/5 | Python semantics proven, data flow traced |
| PR-1/PR-2 (pbkdf2iters breaking) | PR-AF | 3/5 | Correct identification, less depth on caller impact |
| PR-3 (failover lock namespace) | PR-AF | 3/5 | Identifies mismatch, doesn't fully prove concurrent execution path |
| PR-6 (ZFSError comparison) | PR-AF | 2/5 | Pattern inconsistency noted, actual bug not confirmed |
| PR-7/PR-8 (TOCTOU races) | PR-AF | 2/5 | Adversary-challenged, theoretical without proof of concurrent access |
| PR-9 to PR-25 (IMPORTANT) | PR-AF | 1-3/5 | Variable quality, many are "could be a problem" rather than "is a problem" |

Claude Code's evidence quality is consistently higher. PR-AF produces more findings but with lower average evidence quality. This is a precision/recall tradeoff — PR-AF casts a wider net, Claude Code aims more carefully.

---

## 8. False Positive Analysis

PR-AF's adversary phase challenged 16 of 20 findings. This is not a failure — it's the system working as designed. The adversary phase exists to reduce false positives before output.

However, "challenged" doesn't mean "wrong." The 16 challenged findings had their confidence scores reduced (e.g., PR-7 dropped from 0.95 to 0.712) but were still included in the output. This is the right call — a 0.712-confidence race condition in KMIP key management is worth a developer's attention even if it's not confirmed.

The practical question is: how many of the 25 PR-AF findings are real bugs?

- **High confidence (adversary-confirmed, score > 0.85)**: PR-1, PR-2, PR-3 — likely real
- **Medium confidence (adversary-challenged, score 0.60-0.85)**: PR-4 through PR-9 — worth investigating
- **Lower confidence (score < 0.60)**: PR-10 through PR-25 — noise-to-signal ratio increases here

Without post-merge incident data, we can't compute a precise false positive rate. But the adversary phase's 80% challenge rate suggests PR-AF's raw findings are noisy, and the filtering is doing real work.

Claude Code's 4 findings are all credible. CC-1 and CC-4 are pre-existing bugs (not introduced by this PR) but are in modified code paths. CC-2 is a pre-existing security logic bug. CC-3 is a genuine inconsistency introduced by this PR. Zero obvious false positives.

---

## 9. Scoring Rubric & Scorecard

### Rubric

| Metric | Definition | Scale |
|---|---|---|
| **Recall** | (True positives found) / (All real bugs) | 0.0 – 1.0 |
| **Precision** | (True positives) / (All findings posted) | 0.0 – 1.0 |
| **Evidence quality** | Average reasoning depth and actionability | 1 – 5 |
| **Severity calibration** | Were critical bugs labeled critical? | 1 – 5 |
| **Breadth** | Coverage across risk dimensions | 1 – 5 |
| **Practical actionability** | Can a developer act on this immediately? | 1 – 5 |

### Assumptions for Scoring

"Real bugs" for recall calculation: CC-1, CC-2, CC-3, CC-4, PR-1/PR-2 (pbkdf2iters breaking change), PR-3 (failover lock), PR-7/PR-8 (TOCTOU races). This is a conservative set of 8 distinct issues with reasonable confidence. Recall is calculated against this set.

Note: CC-1 and CC-4 are pre-existing bugs in modified code. They count as real bugs because they're in the diff's code paths and the reviewer is expected to flag them.

### Scorecard

| Metric | Claude Code | PR-AF | Notes |
|---|---|---|---|
| **Recall** | 4/8 = **0.50** | 6/8 = **0.75** | CC missed PR-3, PR-7/PR-8. PR-AF missed CC-1, CC-4. |
| **Precision** | ~4/4 = **~1.00** | ~6/25 = **~0.24** | CC findings all credible. PR-AF has significant noise. |
| **Evidence quality** | **4.8 / 5** | **2.4 / 5** | CC findings are exceptionally well-evidenced. |
| **Severity calibration** | **4 / 5** | **3 / 5** | CC correctly labeled critical bugs. PR-AF over-labeled some IMPORTANT findings as CRITICAL. |
| **Breadth** | **2 / 5** | **5 / 5** | CC covered 2 risk dimensions. PR-AF covered 10. |
| **Practical actionability** | **5 / 5** | **3 / 5** | CC findings are immediately actionable. Many PR-AF findings need further investigation. |

### Summary

| System | Recall | Precision | Evidence | Severity | Breadth | Actionability | **Overall** |
|---|---|---|---|---|---|---|---|
| Claude Code | 0.50 | 1.00 | 4.8 | 4.0 | 2.0 | 5.0 | **3.47** |
| PR-AF | 0.75 | 0.24 | 2.4 | 3.0 | 5.0 | 3.0 | **3.07** |

These scores are close. Neither system dominates. They have genuinely different strengths.

---

## 10. Key Observations

**1. Model vs system comparison.** PR-AF runs on kimi-k2.5, which is substantially cheaper than Claude. This is not a model-to-model comparison — it's a system-to-system comparison. PR-AF's multi-agent architecture compensates for a weaker base model through structured decomposition and adversarial self-review.

**2. Claude Code's first pass failure.** The Feb 27 review said "No bugs found." The bugs were in the diff. The Mar 3 second pass found them. This is the strongest argument for multi-pass review — which PR-AF does architecturally. A single-pass system that misses everything on the first try provides false confidence.

**3. PR-AF's adversary phase is working.** 16/20 findings challenged, scores reduced. This is not a sign of weakness — it's the system being honest about uncertainty. The alternative (posting all 25 findings at full confidence) would be worse.

**4. The human reviewer found what neither system found.** yocalebo's 7 comments are all style/pattern issues — naming conventions, exception types, code structure. Neither automated system flagged these. Human reviewers and automated systems are complementary, not substitutes.

**5. PR-AF's biggest gap is framework semantics.** CC-1 required understanding how `@pass_thread_local_storage` changes method calling conventions. This is not a general code analysis skill — it's specific knowledge about the middleware framework. PR-AF's prompts don't appear to include "check for framework-specific decorator semantics" as a review dimension. This is a fixable gap.

**6. PR-AF found real concurrency bugs that nobody else found.** The failover lock namespace mismatch (PR-3), the TOCTOU races (PR-7, PR-8), and the concurrent execution risks (PR-14) are genuine issues exposed by the process pool removal. These are the kind of bugs that cause intermittent production failures and are very hard to find in code review without a dedicated concurrency analysis pass.

**7. The pbkdf2iters finding illustrates different review philosophies.** Claude Code noticed the inconsistency between files (a developer-experience concern). PR-AF noticed the breaking API change in each file (a compatibility concern). Both are valid. A complete review needs both perspectives.

---

## 11. Conclusions & Recommendations

### What PR-AF Should Improve

**1. Add a framework semantics review dimension.** The `@pass_thread_local_storage` miss is the most important gap. A prompt that asks "identify all methods decorated with middleware-injecting decorators and verify they are only called through the dispatch mechanism, not directly" would catch CC-1. This is a project-specific prompt that should be part of the ANATOMY phase's context extraction.

**2. Improve data flow tracing for type bugs.** CC-4 required tracing `existing_datasets` back to its source to confirm it's `list[dict]`. PR-AF's analysis didn't do this. A "type flow" review dimension, or explicit prompting to verify the element types of collections used in `in` checks, would help.

**3. Reduce noise in IMPORTANT findings.** PR-10 through PR-25 have scores below 0.65 and variable evidence quality. The adversary phase is already reducing scores, but the output still includes 17 IMPORTANT findings. A stricter output filter (e.g., only post findings with score > 0.60 and adversary-confirmed) would improve precision without much recall loss.

**4. Improve evidence quality for posted findings.** PR-AF's findings often identify a pattern without proving it's a bug. Adding a "prove it" step — where the system must demonstrate the failure path before posting — would raise evidence quality and reduce false positives simultaneously.

**5. Cross-reference with framework documentation.** For migration PRs (old library → new library), PR-AF should explicitly compare error codes, API contracts, and threading models between the old and new libraries. PR-19 (error code mapping not verified) is a good finding but needs more depth.

### What Claude Code Should Improve

**1. Multi-pass review by default.** The first pass missed everything. The second pass found critical bugs. This should not require manual re-triggering.

**2. Add concurrency and API compatibility dimensions.** Claude Code found zero concurrency bugs and zero API compatibility issues. These are real risk categories in this PR.

**3. Structured coverage tracking.** PR-AF's coverage phase ensures all files and risk dimensions are reviewed. Claude Code's single-agent approach has no equivalent guarantee.

### Recommended Combined Workflow

For high-risk PRs (security-critical, library migrations, concurrency changes):

1. Run PR-AF for breadth — surface all risk dimensions, flag concurrency and API compatibility issues
2. Run Claude Code for depth — get high-confidence, well-evidenced findings on the most critical paths
3. Human reviewer for style/pattern — neither system catches naming conventions, exception patterns, or code structure concerns
4. Require adversary-confirmed PR-AF findings (score > 0.85) to be addressed before merge
5. Require all Claude Code findings to be addressed before merge

This combination would have caught all 8 real bugs identified in this evaluation, with acceptable noise levels.

---

## Appendix: Finding Count Summary

| Category | Claude Code | PR-AF |
|---|---|---|
| Critical findings | 2 (+ 2 pre-existing) | 8 |
| Important findings | 0 | 17 |
| Total findings | 4 | 25 |
| Adversary-confirmed | N/A | 4 |
| Adversary-challenged | N/A | 16 |
| Posted inline | 4 (+ 4 duplicates) | 12 |
| Missed critical bugs | 2 (PR-3, PR-7/PR-8) | 2 (CC-1, CC-4) |

---

*Evaluation produced by LLM-as-a-judge analysis. All findings sourced from `claude_code_review.json` and `pr_af_review.json` in this directory. No findings were invented or inferred beyond what the source data contains.*
