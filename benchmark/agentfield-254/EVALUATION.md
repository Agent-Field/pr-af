# PR-AF Architecture Progression & Evaluation
## Target: AgentField PR #254 (Config Storage Migration)

**Evaluation Date**: 2026-03-11
**Systems Compared**: PR-AF (Current Version, Kimi k2.5) vs. Claude Code (Single-agent baseline)
**Goal**: To document the architectural improvements made to PR-AF and demonstrate how composite multi-agent reasoning out-performs a single-agent baseline like Claude Code in depth, precision, and systemic insight.

---

## 1. Executive Summary

This document evaluates the current version of **PR-AF (Pull Request Agent Field)** against a standard single-agent approach (**Claude Code**). The target is AgentField PR #254, a complex 28-file migration from local JSON config to a SQLite-backed storage model.

The core finding is that **multi-agent composite reasoning (PR-AF) discovers critical systemic vulnerabilities and compound attack chains that a single agent (Claude Code) cannot perceive.** 

While Claude Code successfully catches surface-level mechanical errors (missing parameters, unused variables) in seconds for ~$0.50, PR-AF acts as a deep architectural auditor. Through its progression of architectural improvements—culminating in a Hybrid Evidence Grounding layer and Parallel Compound Analysis—PR-AF achieved a **0% false positive rate** while synthesizing a multi-vector authentication bypass chain that would result in complete system compromise.

### High-Level Comparison (Current Version vs. CC)

| Metric | Claude Code | PR-AF (Current Version) |
|---|---|---|
| **Architecture** | Single-agent, fast context window | 8-Phase Multi-Agent DAG |
| **Duration** | ~5-10 minutes | ~45-50 minutes |
| **Cost** | ~$0.50 - $2.00 | ~$0 (opencode / OSS models) |
| **Surface Bugs Caught**| Yes (e.g., interface mismatches) | Yes |
| **Systemic Flaws** | Missed | **Found** (inconsistent protection) |
| **Compound Risks** | Missed | **Found** (coordinated config injection) |
| **False Positive Rate**| High (relies on assumptions) | **0%** (via Evidence Verifier) |

---

## 2. The PR-AF Architectural Journey

To understand why the Current Version performs so well, we must trace the improvements made to the PR-AF pipeline. We ran 4 successive iterations of the pipeline against the exact same PR to measure the impact of each architectural upgrade.

### Run 1: The Baseline (Sonnet 4.6)
* **Architecture:** Basic Intake → Anatomy → Review Dimensions (no deep context) → Cross-Ref Scoring → Synthesis.
* **Result:** 20 findings, 3 critical, ~35 minutes.
* **Flaw:** High false positive rate (~10%). The agents relied on the diff text and guessed how it interacted with the wider repo, leading to hallucinated claims about error handling.

### Run 2: Enriched Context (Kimi k2.5)
* **Improvement:** Replaced static prompts with **Investigative Prompts**. The harness was explicitly instructed to browse the repository (`cwd=repo_path`), read imports, and verify function signatures before writing findings.
* **Result:** 25 findings, 8 critical, ~40 minutes.
* **Flaw:** Signal rate improved to 88%, but false positives still existed (4%). Agents were *told* to investigate, but LLMs are lazy—they often relied on assumptions instead of actually grepping the repo.

### Run 3: Hybrid Evidence Grounding Layer
* **Improvement:** Introduced the **HUNT → PROVE** adversarial tension. We added a programmatic extraction layer (using fast Python AST parsing) to pull exact caller snippets, import contexts, and cross-references. We fed this raw data into an **Evidence Verifier** harness, forcing it to falsify claims that lacked concrete proof.
* **Result:** 25 findings, 7 critical, ~43 minutes.
* **Impact:** **False Positive Rate dropped to 0%.** The verifier correctly dropped assumptions that couldn't be backed up by the extracted code snippets.

### Run 4: Current Version (Compound Analysis & Dedup)
* **Improvement:** The original `cross_ref` phase was a naive scoring multiplier that wasted 34% of the pipeline time (16 minutes) without changing any finding rankings. We replaced it with **Parallel Compound Analysis**. The system groups related findings into clusters (by file, import, caller, or tag) and spawns parallel investigators to see if the combination of minor bugs creates a major exploit. A final `compound_dedup_phase` collapses duplicate insights.
* **Result:** 17 findings, 13 critical. Cross-ref time reduced from 16m → 5m.
* **Impact:** Discovered **3 genuinely novel, critical insights** (see Section 3) that no individual reviewer agent found.

---

## 3. The Power of Compound Analysis

The most significant differentiator between PR-AF and Claude Code is the **Phase 5.5: Compound Analysis**. 

In PR #254, individual reviewers found several isolated issues in `config_db.go`:
1. `AdminToken` can be overridden from the database.
2. `APIKey` lacks protection from database merge.
3. `WebhookSecret` is merged blindly from the database.

A single agent (Claude Code) sees these as three separate, medium-severity bugs ("Hey, you forgot to protect this field"). 

The **PR-AF Compound Analyzer** was handed this cluster of findings along with their evidence. It recognized the systemic pattern and synthesized a **first-class critical finding**:

> **Complete System Compromise via Coordinated DB Config Injection**
> *Severity: Critical | Score: 1.104*
> The combination of multiple unprotected security-sensitive fields in the DB config merge logic creates a complete authentication and authorization bypass chain. An attacker with database write access can simultaneously inject malicious values for: (1) DID Authorization tokens, (2) API Keys, and (3) Webhook secrets. This is not an isolated missing validation, but a systemic control gap where the protection pattern applied to the `Storage` config was neglected across all authentication vectors.

Claude Code cannot make this leap because it lacks the architectural design to group, step back, and re-evaluate findings in relation to one another. 

---

## 4. PR-AF Current Version vs. Claude Code (CC)

### Depth vs. Speed
* **Claude Code** is exceptional for the "inner loop" of development. If an engineer forgets a parameter or misnames a variable, CC finds it in seconds and fixes it inline.
* **PR-AF** is designed for the "outer loop" (the CI/CD gate). It takes 45 minutes because it performs exhaustive, multi-dimensional analysis (Semantic, Mechanical, Systemic), programmatic evidence extraction, and adversarial challenges.

### Precision (False Positives)
* **Claude Code** relies on its context window. If a referenced function isn't in the window, it guesses based on naming conventions. This creates false positives that human reviewers have to dismiss.
* **PR-AF** uses an **Evidence Grounding Layer**. If a semantic reviewer claims a bug exists, the extraction engine pulls the exact AST node, and the Verifier tests the claim. In our benchmarks, PR-AF's current version achieved a 0% false positive rate on PR #254.

### The Verdict
Our multi-reasoner architecture proves that **intelligence is in the composition, not just the model**. By structuring the workflow into parallel hunters, programmatic evidence extraction, adversarial verification, and compound synthesis, PR-AF transforms an average LLM into a senior architectural auditor.
