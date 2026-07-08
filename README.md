<div align="center">

# PR-AF

### Open-Source Agentic Code Review Built on [AgentField](https://github.com/Agent-Field/agentfield)

[![Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-16a34a?style=for-the-badge)](LICENSE)
[![Python](https://img.shields.io/badge/python-3.11%2B-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://www.python.org/downloads/)
[![Built with AgentField](https://img.shields.io/badge/Built%20with-AgentField-0A66C2?style=for-the-badge)](https://github.com/Agent-Field/agentfield)
[![More from Agent-Field](https://img.shields.io/badge/More_from-Agent--Field-111827?style=for-the-badge&logo=github)](https://github.com/Agent-Field)

<p>
  <a href="#benchmark-position">Benchmark</a> •
  <a href="#one-call-dx">One-Call DX</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#ecosystem-comparison">Comparison</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="docs/ARCHITECTURE.md">Architecture</a>
</p>

</div>

PR-AF is the **#1 open-source code reviewer on Martian Code-Review-Bench**. It is built
for deep code review, not shallow diff summaries: turn each PR into a task-specific
review plan, spawn focused reviewer agents, ground findings in code evidence, challenge
the results, and squeeze more useful review intelligence out of cheaper models. Run
DeepSeek-class models for routine PRs, GLM-5.2 for deep open-model reviews, or Opus-class
frontier models for major PRs — where PR-AF tops the benchmark by a wide margin.

<p align="center">
  <img src="assets/hero.png" alt="PR-AF — open-source agentic code review benchmark" width="100%" />
</p>

## Benchmark Position

On the 38 runnable Martian Code-Review-Bench PRs, **PR-AF with GLM-5.2 is the
#1 open-source reviewer in golden recall**: 0.706 across 42 compared tools. It is ahead
of cubic-v2 and every qodo, coderabbit, greptile, copilot, and devin variant in this
snapshot.

Where PR-AF shines:

| strength | result |
|---|---|
| **Known bug recall** | 0.706 golden recall — #1 open source across 42 compared tools. |
| **More real issues found** | 595 independently valid findings, ~3× more than the leading commercial tools in the adjusted comparison. |
| **Open + reproducible** | Single open model (`GLM-5.2`), public results, per-PR judge verdicts, and reproduction scripts. |
| **Self-hosted API** | Run locally with Docker; trigger reviews by CLI, curl, CI, or other agents. |
| **Model-flexible** | Use cheaper models for regular PRs, GLM-5.2 for open-model CI gates, and Opus-class frontier models for highest-stakes reviews. |
| **Frontier ceiling** | With Opus-class commercial models, PR-AF tops the benchmark by a wide margin. |
| **Cost position** | About 10× cheaper per review than closed-source tools. |

Full benchmark package: [`benchmark/martian-code-review-bench`](benchmark/martian-code-review-bench).

## One-Call DX

Trigger it with the `af` CLI (requires af ≥ 0.1.87) — it streams live progress and prints the result:

```bash
af call pr-af.review --in '{"pr_url": "https://github.com/owner/repo/pull/123"}'
```

Prefer raw HTTP? Hit the API directly with curl:

```bash
curl -X POST http://localhost:8080/api/v1/execute/async/pr-af.review \
  -H "Content-Type: application/json" \
  -d '{"input": {"pr_url": "https://github.com/owner/repo/pull/123"}}'
```

Posts inline GitHub review comments with evidence-grounded findings:

```jsonc
{
  "total_findings": 5,
  "by_severity": {"critical": 1, "important": 2, "suggestion": 2},
  "findings": [
    {
      "severity": "critical",
      "title": "SQL injection in user input handling",
      "file": "src/api/users.py",
      "line": 42,
      "body": "Raw query parameter interpolated directly into SQL. Tracer confirms no parameterization between input and cursor.execute().",
      "suggestion": "cursor.execute('SELECT * FROM users WHERE id = %s', (user_id,))",
      "evidence": "AST extraction confirms f-string SQL at users.py:42, no sanitization in call chain",
      "compound_risk": "Combined with missing auth middleware (finding #2), this is exploitable by unauthenticated users"
    }
  ],
  "review_dimensions": 4
}
```

Custom review strategy per PR. Evidence-grounded findings. About 10× cheaper per review than closed-source tools.

---

## Dynamic Pipeline Architecture

PR-AF does not execute a static script. It structurally morphs its own execution graph based on the topology of the incoming Pull Request.

When a PR arrives, the system dynamically compiles review dimensions — evaluating the diff through semantic, mechanical, and systemic lenses. It uses these dimensions to spawn specialized, ephemeral reviewer agents tailored exclusively to the exact context of the current PR.

<p align="center">
  <img src="assets/architecture.png" alt="PR-AF 7-Phase Adaptive Pipeline" width="100%" />
</p>

> Full architecture deep-dive: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)

<details>
<summary><strong>Pipeline flow (Mermaid)</strong></summary>

```mermaid
graph TD
    classDef intake fill:#f3f4f6,stroke:#4b5563,stroke-width:2px;
    classDef dynamic fill:#dbeafe,stroke:#3b82f6,stroke-width:2px;
    classDef verify fill:#fef3c7,stroke:#2563eb,stroke-width:2px;
    classDef synthesize fill:#ede9fe,stroke:#d97706,stroke-width:2px;
    classDef output fill:#ecfdf5,stroke:#8b5cf6,stroke-width:2px;

    PR[Incoming Pull Request] --> I1[Intake Triage]:::intake
    I1 --> A1[Topological Anatomy Mapping]:::intake
    
    A1 --> M1[Semantic Lens Generator]:::dynamic
    A1 --> M2[Mechanical Lens Generator]:::dynamic
    A1 --> M3[Systemic Lens Generator]:::dynamic
    
    M1 --> D[Dimension Deduplication & Compilation]:::dynamic
    M2 --> D
    M3 --> D
    
    D -->|Dynamically spawns N dimensions| R1(Thread 1: State Mutation)
    D --> R2(Thread 2: API Boundaries)
    D --> R3(Thread N: Dynamic Context...)
    
    R1 --> E[Programmatic AST Extraction Engine]:::verify
    R2 --> E
    R3 --> E
    
    E -->|Ground truth caller snippets| V[Evidence Verification Layer]:::verify
    V -->|Unsubstantiated claims pruned| F[Falsifiability Gate]:::verify
    
    F --> C1(Compound Cluster: File Topology)
    F --> C2(Compound Cluster: Shared Imports)
    F --> C3(Compound Cluster: Tag Overlap)
    
    C1 --> S[Compound Vulnerability Synthesis]:::synthesize
    C2 --> S
    C3 --> S
    
    S --> L{Coverage Depth Gate}
    L -->|Blind spots detected| I1
    L -->|Full coverage achieved| O[Synthesized GitHub Annotations]:::output
```

</details>

---

## How It Works

PR-AF uses this multi-phase cognitive pipeline to ensure rigorous, high-fidelity reviews:

### 1. Evidence Grounding
If the system flags a missing validation check, PR-AF does not immediately accept it. It pulls exact caller snippets and import context from the repository, then verifies whether the finding is grounded in the code before it reaches the final review.

### 2. Compound Vulnerability Synthesis
Standard tools analyze code linearly. PR-AF clusters related risks across files and evaluates whether isolated findings combine into a larger systemic issue.

### 3. Falsifiability Gates
Before a finding becomes a GitHub comment, the system tries to invalidate it: safe behavior, intended behavior, existing mitigations, or weak evidence. Findings that survive are returned with file, line, body, suggestion, and evidence.

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

## Quick Start

```bash
git clone https://github.com/Agent-Field/pr-af.git && cd pr-af
cp .env.example .env          # Add OPENROUTER_API_KEY, GH_TOKEN
docker compose up --build
```

Starts AgentField control plane (`http://localhost:8080`) + PR-AF agent.

```bash
curl -X POST http://localhost:8080/api/v1/execute/async/pr-af.review \
  -H "Content-Type: application/json" \
  -d '{"input": {"pr_url": "https://github.com/owner/repo/pull/123"}}'
```

Poll for results:

```bash
curl http://localhost:8080/api/v1/executions/<execution_id>
```

## GitHub Actions Integration

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

---
## From the AgentField AF Ecosystem

PR-AF is one example of a larger AgentField ecosystem for building autonomous, agent-native systems.

* **[SWE-AF](http://www.agentfield.ai/github/swe-af)**: autonomous software factory for production ready PR
* **[SEC-AF](http://www.agentfield.ai/github/sec-af)**: autonomous security factory for code security review, vulnerability investigation, and evidence-grounded remediation.

## From the AgentField Blog

### [How an AI-Native Engineering Team Does Code Review](https://www.agentfield.ai/blog/ai-native-code-review?utm_source=github-readme&utm_campaign=pr-af-readme&utm_id=pr-af-readme-blog-ai-native-code-review)

When the writer and the reviewer are the same intelligence, the pull request gate stops doing what it was designed to do.

<p align="center">
  <a href="https://www.agentfield.ai/blog/ai-native-code-review?utm_source=github-readme&utm_campaign=pr-af-readme&utm_id=pr-af-readme-blog-ai-native-code-review">
    <img src="assets/blog-ai-native-code-review.webp" alt="An engineer reading a pull request through a telescope of risk dimensions." width="100%" />
  </a>
</p>

[Read the post →](https://www.agentfield.ai/blog/ai-native-code-review?utm_source=github-readme&utm_campaign=pr-af-readme&utm_id=pr-af-readme-blog-ai-native-code-review)
