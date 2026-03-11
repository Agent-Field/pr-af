<div align="center">

# PR-AF

### AI-Native Pull Request Review Pipeline Built on [AgentField](https://github.com/Agent-Field/agentfield)

[![Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-16a34a?style=for-the-badge)](LICENSE)
[![Python](https://img.shields.io/badge/python-3.11%2B-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://www.python.org/downloads/)
[![Built with AgentField](https://img.shields.io/badge/Built%20with-AgentField-0A66C2?style=for-the-badge)](https://github.com/Agent-Field/agentfield)
[![More from Agent-Field](https://img.shields.io/badge/More_from-Agent--Field-111827?style=for-the-badge&logo=github)](https://github.com/Agent-Field)

<p>
  <a href="#the-ai-code-review-problem">The Problem</a> •
  <a href="#architectural-intelligence">Why It Works</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="benchmark/agentfield-254/EVALUATION.md">Benchmarks</a>
</p>

</div>

Single-agent code reviewers are fast, but they suffer from high false-positive rates and fail to grasp deep, architectural consequences. 

**PR-AF** (Pull Request Agent Field) is a specialized multi-agent Directed Acyclic Graph (DAG) that operates as a **Deep Architectural Auditor**. By composing parallel hunters, programmatic evidence extraction, adversarial verification, and compound synthesis, PR-AF transforms an average LLM into a senior-level code reviewer capable of detecting systemic zero-day vulnerabilities. 

*Zero false positives. Deep architectural insights. One API call.*

---

## The AI Code Review Problem

Most "AI PR Reviewers" just pipe a git diff into a single LLM prompt and ask it to find bugs. This creates three fatal flaws:

1. **The Hallucination Trap:** Without traversing the actual repository, the LLM guesses how functions interact. It flags missing error handling that is actually handled cleanly three layers up. You get noisy, frustrating false positives.
2. **The Myopic View:** They spot surface-level, mechanical errors (e.g., missing parameters) but miss systemic flaws. They cannot see how three isolated, seemingly benign changes combine to create an exploit.
3. **The Static Prompt Ceiling:** Using the same "You are an expert reviewer" prompt for a CSS tweak and a highly concurrent database migration yields generic, unhelpful advice.

## Architectural Intelligence

PR-AF is built on the [Composite Intelligence](https://github.com/Agent-Field/agentfield) philosophy. We don't rely on a magical, omniscient model. Instead, the intelligence is encoded in the **architecture itself**. 

By composing ~100 synchronous micro-agent tasks, PR-AF achieves results impossible for a single agent.

### 1. Evidence Grounding: Achieving Zero False Positives
LLMs are notoriously lazy and eager to please. If you ask them to find a bug, they will invent one. PR-AF solves this using a **Hybrid Evidence Layer**. 

When an agent flags a vulnerability, PR-AF uses fast programmatic AST parsing to extract the exact caller snippets, import contexts, and cross-references from the repository. This hard data is fed into an isolated **Evidence Verifier** harness. Its sole job: prove the first agent wrong. If a claim cannot be irrefutably backed by the extracted code snippets, it is silently dropped. **In our benchmarks, this dropped the false positive rate to 0%.**

### 2. Synthesizing Compound Attack Chains
Where a single-agent reviewer stops at "you forgot to validate this field," PR-AF looks at the whole board. 

In our benchmark of a complex configuration migration PR, PR-AF's parallel agents found three isolated, medium-severity bugs across different files: an AdminToken override, an APIKey vulnerability, and a WebhookSecret overwrite. 

PR-AF's **Compound Analyzer** clustered these findings together and synthesized a critical zero-day exploit that the single-agent baseline missed entirely:

> **Complete System Compromise via Coordinated DB Config Injection**
> *Severity: Critical | Score: 1.104*
> The combination of multiple unprotected security-sensitive fields in the DB config merge logic creates a complete authentication and authorization bypass chain. An attacker with database write access can simultaneously inject malicious values across all authentication vectors. This is not an isolated missing validation, but a systemic control gap.

### 3. Adversarial Red Teaming
Before any finding reaches the user, it must survive the **Adversary**. This is a cynical "Red Team" agent tasked exclusively with finding reasons why the reported bug is actually safe, intended behavior, or mitigated elsewhere in the codebase. Only findings that survive this adversarial tension make it to the PR comment.

### 4. Dynamic Meta-Dimensions
PR-AF does not use static review prompts. It begins by running the PR through three parallel lenses: *Semantic* (logic/behavior), *Mechanical* (types/signatures), and *Systemic* (architecture/patterns). These meta-selectors dynamically generate custom, highly specific review criteria tailored exactly to the PR's anatomy, which are then used to dispatch the actual reviewing agents.

*(See the full [Evaluation & Benchmarks](benchmark/agentfield-254/EVALUATION.md) against Claude Code)*

---

## Quick Start

### 1. Configure the Agent
Ensure you have the AgentField Control Plane running. Define your environment variables in `.env`:
```bash
GH_TOKEN=ghp_your_github_token  # To post comments
OPENROUTER_API_KEY=sk-or-your_key
HARNESS_MODEL=openrouter/moonshotai/kimi-k2.5
```

### 2. Start the Agent Node
```bash
docker compose up -d
```

### 3. Dispatch a Review
Trigger the review asynchronously through the control plane:

```bash
curl -X POST http://localhost:8080/api/v1/execute/async/pr-af.review \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "pr_url": "https://github.com/Agent-Field/agentfield/pull/254",
      "depth": "standard",
      "dry_run": true
    }
  }'
```

*(When `dry_run: true`, the agent evaluates the PR but does not actually post the comments to GitHub. Check the Control Plane UI to view the final payload).*
