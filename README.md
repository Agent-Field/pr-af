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

Most AI code reviewers are incredibly fast and great at spotting surface-level errors (like typos, missing parameters, or unhandled exceptions). 

However, when evaluating large, complex Pull Requests, single-prompt AI tools often hit a ceiling: they generate false positives by guessing how functions interact outside the diff, and they miss systemic vulnerabilities that span across multiple files.

**PR-AF** (Pull Request Agent Field) is a specialized, multi-agent pipeline designed to act as a **deep architectural auditor**. Instead of relying on a single large language model call, PR-AF orchestrates dozens of micro-agents that extract hard evidence from your repository, debate findings, and synthesize compound attack chains.

*Zero false positives. Deep architectural insights. Open-source and BYOK (Bring Your Own Keys).*

---

## How It Works

PR-AF uses a multi-phase agentic pipeline to ensure rigor and depth:

### 1. Evidence Grounding (0% False Positives)
LLMs tend to make assumptions. If an agent thinks a parameter is missing, PR-AF doesn't immediately post a comment. Instead, it uses programmatic code extraction to pull the exact caller snippets and import contexts from your repository. An isolated **Evidence Verifier** agent is then forced to validate the claim against this ground-truth data. If the evidence doesn't support the bug, it's silently dropped.

### 2. Compound Analysis
Standard tools look at issues in isolation. PR-AF clusters isolated, seemingly minor findings across different files to see if they combine into a larger exploit. For example, finding an unprotected API key in one file and a database merge vulnerability in another will be synthesized into a single critical "Coordinated Injection" finding.

### 3. Adversarial Red Teaming
Before any finding makes it to your GitHub PR, it must survive the **Adversary**—a cynical "Red Team" agent tasked exclusively with trying to prove why the reported bug is actually safe, intended behavior, or mitigated elsewhere in the codebase.

---

## Ecosystem Comparison

There are excellent AI code review tools on the market. PR-AF is not designed to replace fast, interactive tools; it is designed for comprehensive CI/CD gating where accuracy and architectural depth matter more than speed.

| Feature | PR-AF (AgentField) | Claude Code CLI | Commercial SaaS (e.g. Codex, CodeRabbit) |
|---|---|---|---|
| **Best For** | Deep CI/CD architectural audits | Fast, iterative inner-loop development | Clean GitHub UX and chat-based reviews |
| **Cost** | **Free / Open Source** (BYOK API costs only) | Pay-per-token (BYOK) | ~$20 - $25 / user / month |
| **Architecture** | Multi-agent orchestrated pipeline | Single-agent interactive loop | Agentic retrieval + LLM review |
| **Execution Time**| ~35-50 minutes | Seconds to minutes | ~2-5 minutes |
| **False Positives**| **Extremely low** (Evidence Verifier + Adversary) | Moderate (relies on context window) | Low-to-Moderate (heuristic filtering) |
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

*Note: PR-AF runs a comprehensive multi-agent pipeline. Reviews typically take 35-50 minutes depending on PR complexity.*
