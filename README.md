<div align="center">

# PR-AF

### AI-Native Pull Request Review Pipeline Built on [AgentField](https://github.com/Agent-Field/agentfield)

[![Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-16a34a?style=for-the-badge)](LICENSE)
[![Python](https://img.shields.io/badge/python-3.11%2B-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://www.python.org/downloads/)
[![Built with AgentField](https://img.shields.io/badge/Built%20with-AgentField-0A66C2?style=for-the-badge)](https://github.com/Agent-Field/agentfield)
[![More from Agent-Field](https://img.shields.io/badge/More_from-Agent--Field-111827?style=for-the-badge&logo=github)](https://github.com/Agent-Field)

<p>
  <a href="#the-problem">The Problem</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#compound-analysis">Compound Risk</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="benchmark/agentfield-254/EVALUATION.md">Benchmarks</a>
</p>

</div>

Single-agent code reviewers are fast, but they suffer from high false-positive rates and fail to grasp deep, architectural consequences. 

**PR-AF** (Pull Request Agent Field) is a specialized multi-agent Directed Acyclic Graph (DAG) that operates as a **Deep Architectural Auditor**. By composing parallel hunters, programmatic evidence extraction, adversarial verification, and compound synthesis, PR-AF transforms an average LLM into a senior-level code reviewer capable of detecting systemic zero-day vulnerabilities. 

*Zero false positives. Deep architectural insights. One API call.*

---

## The Problem: The Single-Agent Ceiling

Most AI code reviewers pass a git diff into a single LLM prompt and ask it to find bugs. This approaches the problem at the surface level. 

1. **They hallucinate:** Without full repository context, single agents guess how functions interact.
2. **They are naive:** They spot mechanical errors (e.g., missing parameters) but miss systemic flaws (e.g., a combination of three isolated changes creates an auth bypass).
3. **They lack rigor:** They rarely verify their own assumptions against actual source code.

## How It Works: Composite Intelligence

PR-AF is built on the [Composite Intelligence](https://github.com/Agent-Field/agentfield) philosophy. The intelligence isn't just in the LLM—it's encoded in the **architecture itself**. 

Instead of a single prompt, PR-AF runs an 8-Phase multi-agent pipeline spanning roughly ~100 synchronous micro-agent tasks.

### The 8-Phase Review Pipeline

| Phase | Description | Architecture |
|---|---|---|
| **1. INTAKE** | Classifies PR complexity and depth. | `.ai()` |
| **2. ANATOMY** | Groups the changed files into logical clusters. | `.ai()` |
| **3. META-DIMENSIONS** | Evaluates the PR through 3 parallel lenses (Semantic, Mechanical, Systemic) to dynamically generate custom review prompts tailored to this specific PR. | `.harness()` (3x parallel) |
| **4. REVIEW** | Dispatches specific sub-agents (e.g., a "Security Auditor", an "API Contract Specialist") to investigate the repo based on the custom dimensions. | `.harness()` (Nx parallel) |
| **5. EVIDENCE LAYER** | Uses programmatic Python AST parsing to pull exact caller snippets, import contexts, and cross-references. An **Evidence Verifier** harness attempts to falsify the Reviewers' claims using this ground truth. | Hybrid (Code + `.harness()`) |
| **6. ADVERSARY** | A cynical "Red Team" agent tries to disprove remaining findings. | `.harness()` |
| **7. COMPOUND ANALYSIS** | Clusters isolated, minor findings to determine if their combination creates a novel, first-class exploit chain. | `.harness()` (Nx parallel) |
| **8. SYNTHESIS** | Scores, dedups, filters, and formats the findings as inline GitHub comments. | Code |

## The Differentiator: Compound Analysis

Where single-agent reviewers stop at "you forgot to protect this field," PR-AF looks at the whole board. 

During our benchmarks on a complex configuration migration PR, PR-AF's individual reviewers found three separate, medium-severity bugs: an AdminToken override, an APIKey vulnerability, and a WebhookSecret overwrite. 

The **Phase 7 Compound Analyzer** clustered these findings together, stepped back, and synthesized a critical zero-day exploit that the single-agent baseline missed:

> **Complete System Compromise via Coordinated DB Config Injection**
> *Severity: Critical | Score: 1.104*
> The combination of multiple unprotected security-sensitive fields in the DB config merge logic creates a complete authentication and authorization bypass chain. An attacker with database write access can simultaneously inject malicious values across all authentication vectors. This is not an isolated missing validation, but a systemic control gap.

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
