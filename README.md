<div align="center">

# PR-AF

### Deep Architectural Pull Request Reviewer Built on [AgentField](https://github.com/Agent-Field/agentfield)

[![Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-16a34a?style=for-the-badge)](LICENSE)
[![Python](https://img.shields.io/badge/python-3.11%2B-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://www.python.org/downloads/)
[![Built with AgentField](https://img.shields.io/badge/Built%20with-AgentField-0A66C2?style=for-the-badge)](https://github.com/Agent-Field/agentfield)

<p>
  <a href="#how-it-works">How It Works</a> •
  <a href="#comparison">Comparison</a> •
  <a href="#quick-start">Quick Start</a>
</p>

</div>

Most AI code reviewers are incredibly fast and great at spotting surface-level syntax errors or missing parameters. However, when evaluating large, complex Pull Requests, single-prompt AI tools often hit a ceiling: they generate false positives by guessing how functions interact outside the diff, and they miss systemic vulnerabilities that span across multiple files.

**PR-AF** (Pull Request Agent Field) is a specialized pipeline designed to act as a **deep architectural auditor**. Instead of relying on a single large language model call, PR-AF orchestrates a massively parallel cognitive architecture that extracts hard evidence from your repository, filters out noise, and synthesizes compound attack chains.

*Zero false positives. Deep architectural insights. Open-source and BYOK (Bring Your Own Keys).*

---

## How It Works

PR-AF uses a multi-phase cognitive pipeline to ensure rigorous, high-fidelity reviews:

### 1. Evidence Grounding (0% False Positives)
Language models inherently operate on probability, which leads to assumption-based false positives. If the system flags a missing validation check, PR-AF does not immediately accept it. Instead, it utilizes programmatic AST (Abstract Syntax Tree) extraction to pull the exact caller snippets and import contexts from the broader repository. This raw data is then evaluated through an isolated verification layer. If the initial claim cannot be irrefutably grounded in the extracted code, it is silently pruned.

### 2. Compound Vulnerability Synthesis
Standard tools analyze code linearly. PR-AF looks at the entire board to identify cross-correlated risks. It clusters isolated, seemingly minor anomalies across different files and evaluates them concurrently to detect whether they coalesce into a larger systemic exploit. For example, identifying an unprotected API key in one module and a database merge vulnerability in another will be synthesized into a single, high-severity "Coordinated Injection" finding.

### 3. Falsifiability Gates
Before any finding is compiled into the final GitHub comment, it must pass through a strict falsifiability framework. The system actively attempts to invalidate its own findings—searching for reasons why the reported anomaly might be safe, intended behavior, or securely mitigated elsewhere in the codebase structure. Only findings that survive this aggressive auto-invalidation process are surfaced to the developer.

---

## Ecosystem Comparison

There are excellent AI code review tools on the market. PR-AF is not designed to replace fast, interactive tools; it is designed for comprehensive CI/CD gating where accuracy and architectural depth matter more than execution speed.

| Feature | PR-AF (AgentField) | Claude Code CLI | Commercial SaaS (e.g. Codex, CodeRabbit) |
|---|---|---|---|
| **Best For** | Deep CI/CD architectural audits | Fast, iterative inner-loop development | Clean GitHub UX and chat-based reviews |
| **Cost** | **Free / Open Source** (BYOK API costs only) | Pay-per-token (BYOK) | ~$20 - $25 / user / month |
| **Architecture** | Massively parallel cognitive pipeline | Single-thread interactive loop | Context retrieval + LLM review |
| **Execution Time**| ~35-50 minutes | Seconds to minutes | ~2-5 minutes |
| **False Positives**| **Extremely low** (Evidence Grounding) | Moderate (relies on context window) | Low-to-Moderate (heuristic filtering) |
| **Compound Risks**| **Yes** (Dedicated Compound Synthesizer) | Unlikely (diff-focused) | Partial (depends on retrieval accuracy) |

*We highly recommend using Claude Code for your local development and running PR-AF as your final GitHub Actions gatekeeper.*

---

## Quick Start: GitHub Actions (Zero Config)

The easiest way to use PR-AF is to drop it into your GitHub Actions. It requires **zero configuration** and runs securely using GitHub's built-in `GITHUB_TOKEN`.

Add this workflow to your repository at `.github/workflows/pr-af-review.yml`. It triggers automatically whenever you add the **`pr-af`** label to a Pull Request.

```yaml
name: AgentField PR Review

on:
  pull_request:
    types: [labeled]

jobs:
  pr-af-review:
    if: github.event.label.name == 'pr-af'
    runs-on: ubuntu-latest
    
    # Needs permissions to post comments and read code
    permissions:
      contents: read
      pull-requests: write

    steps:
      - name: Checkout PR-AF
        uses: actions/checkout@v4
        with:
          repository: Agent-Field/pr-af
          path: pr-af

      - name: Start AgentField & PR-AF
        working-directory: ./pr-af
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          docker compose up -d
          sleep 15 # Wait for services to be healthy

      - name: Execute Deep Architectural Audit
        working-directory: ./pr-af
        env:
          PR_URL: ${{ github.event.pull_request.html_url }}
        run: |
          python3 scripts/ci_runner.py
```

*Note: PR-AF runs a comprehensive parallel pipeline. Reviews typically take 35-50 minutes depending on PR complexity.*
