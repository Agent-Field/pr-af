# PR-AF Architecture

PR-AF is a multi-agent pull request reviewer built on [AgentField](https://agentfield.dev). It uses a 7-phase adaptive pipeline that dynamically determines what aspects of a PR to review, spawns parallel reviewer agents with runtime-crafted prompts, challenges its own findings adversarially, and posts specific inline comments to GitHub.

This document explains the architecture for developers who want to understand the design, contribute, or adapt the patterns.

---

## The Core Insight

Most AI code reviewers run a single LLM pass over the diff with a fixed checklist prompt. PR-AF takes a fundamentally different approach: it mirrors how a **senior engineer actually reviews a PR**.

A great reviewer doesn't apply the same checklist to every PR. They:
1. Understand what the PR is trying to do and how big/complex it is
2. Identify which parts of the change are risky and WHY they're risky for THIS specific PR
3. Review different aspects in parallel (security, correctness, style) but only where relevant
4. Think about cross-cutting interactions ("does this change break that assumption?")
5. Challenge their own findings ("is this a real issue or am I nitpicking?")
6. Check what's MISSING (tests, error handling, docs)
7. Write specific, actionable comments at the exact lines that matter

PR-AF encodes this process as a multi-agent pipeline where the **review strategy emerges from the content, not from a fixed configuration**.

**What makes it different:**

- **Dynamic review dimensions.** No hardcoded reviewer categories. The planner examines the PR and REASONS about what aspects need review, then crafts specific investigation prompts at runtime. A PR touching auth gets security-focused reviewers. A PR refactoring logging gets consistency-focused reviewers. The review shape adapts to the PR shape.
- **Cross-change interaction detection.** A dedicated agent watches for interactions between findings from different reviewers — the most valuable and hardest-to-automate part of expert PR review.
- **Adversarial tension.** Separate agents find issues and challenge them. This dramatically reduces noise and false positives — the #1 complaint about AI code reviewers.
- **AI-PR awareness.** Special detection and handling for AI-generated code, which has characteristic failure modes that human-written code does not.
- **Deterministic scoring.** LLMs reason about issues; code computes severity and priority. Same findings always produce same scores.
- **Streaming pipeline.** The review layer starts consuming findings as reviewers produce them, overlapping work across phases.

---

## Pipeline Overview

The pipeline runs in 7 phases. Phases 1-3 are sequential (each builds on the previous). Phases 4-5 overlap via streaming. Phases 6-7 run after all findings are finalized.

```
┌─────────────────────────────────────────────────────────────────────┐
│                         PR-AF Pipeline                              │
│                                                                     │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐                        │
│  │ Phase 1  │──▶│ Phase 2  │──▶│ Phase 3  │                        │
│  │ INTAKE   │   │ ANATOMY  │   │ PLANNING │                        │
│  │ .ai()+fb │   │ code+hrn │   │ .harness │                        │
│  └──────────┘   └──────────┘   └────┬─────┘                        │
│                                     │                               │
│                    ┌────────────────┼────────────────┐              │
│                    ▼                ▼                ▼              │
│              ┌──────────┐   ┌──────────┐   ┌──────────┐            │
│  Phase 4:    │Reviewer A│   │Reviewer B│   │Reviewer C│  ...       │
│  PARALLEL    │.harness()│   │.harness()│   │.harness()│            │
│  REVIEW      └────┬─────┘   └────┬─────┘   └────┬─────┘            │
│                   │              │              │                   │
│                   └──────────────┼──────────────┘                   │
│                                  │  findings stream                 │
│                                  ▼  (asyncio.Queue)                 │
│              ┌──────────────────────────────────────┐               │
│  Phase 5:    │  Cross-Ref     Adversary    Coverage │               │
│  REVIEW      │  Resolver      Reviewer      Gate   │               │
│  LAYER       │  .harness()   .harness()    .ai()   │               │
│              └──────────────────┬───────────────────┘               │
│                                 │                                   │
│                    ┌────────────┼────────────┐                      │
│                    ▼                         ▼                      │
│              ┌──────────┐             ┌──────────┐                  │
│  Phase 6:    │SYNTHESIS │  Phase 7:   │  OUTPUT  │                  │
│              │  (code)  │──────────▶  │  (code)  │                  │
│              └──────────┘             └──────────┘                  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Phase 1: Intake

**Primitive:** `.ai()` with `.harness()` fallback
**Purpose:** Classify the PR to drive downstream routing.

The intake classifier reads PR metadata (title, description, labels, commit messages) and diff summary statistics to determine: what kind of PR is this, how complex is it, and what areas does it touch?

Classification uses a fast `.ai()` path for clear-cut PRs. When the PR description is vague, the diff is massive, or signals are mixed, it automatically escalates to a `.harness()` that can navigate the actual diff to understand what's going on.

A critical intake signal is **AI-generation detection**. The classifier looks for characteristic patterns of AI-generated code (see [AI-PR Handling](#ai-pr-handling)) and produces a confidence score. This score doesn't trigger a separate review pass — it adjusts the review plan across ALL dimensions (more skepticism, different focus areas).

```python
class IntakeResult(BaseModel):
    """Structured JSON — drives routing decisions downstream."""
    pr_type: str          # feature | bugfix | refactor | docs | infra | mixed
    complexity: str       # trivial | standard | complex | massive
    languages: list[str]  # detected from diff file extensions + content
    areas_touched: list[str]  # semantic areas: auth, database, api, frontend, config...
    risk_signals: list[str]   # detected risk indicators: "touches auth", "modifies schema"
    ai_generated: float   # 0.0-1.0 confidence that PR is AI-generated
    review_depth: str     # quick | standard | deep (drives budget allocation)
    confident: bool       # False → escalate to .harness()
```

**Why `.ai()` first:** PR metadata (title + description + stats) is typically < 500 tokens. Fast classification handles 80%+ of PRs. The `.harness()` fallback handles the rest — massive PRs, missing descriptions, ambiguous changes.

---

## Phase 2: Anatomy

**Primitive:** Code (programmatic) + `.harness()` (semantic)
**Purpose:** Build structural understanding of the changes.

The anatomy phase has two sub-steps that can run in parallel:

### 2a: Structural Analysis (Code — NOT LLM)

Programmatic diff parsing and dependency analysis. This is computation, not reasoning — do it in code:

- **Diff parsing:** Decompose the diff into files, hunks, lines. Identify added/removed/modified lines per file. Detect file renames/moves.
- **Change clustering:** Group related files by directory, module, or import relationship. Changes to `src/auth/` files form a cluster. Changes to `tests/test_auth.py` associate with that cluster.
- **Blast radius computation:** Build a dependency graph from import/require statements. For each changed file, find files that import it, files that it imports, and files that share type dependencies. The blast radius is everything that COULD be affected by the changes but isn't in the diff itself.
- **Statistics:** Lines added/removed/modified per file, per cluster, total. File types. Test-to-code ratio.

```python
class DiffStructure(BaseModel):
    """Structured JSON — programmatic consumption by planner."""
    files: list[FileChange]        # Per-file change details
    clusters: list[ChangeCluster]  # Grouped related changes
    blast_radius: list[str]        # Files affected but not changed
    dependency_graph: dict[str, list[str]]  # import relationships
    stats: DiffStats               # Aggregate statistics
```

### 2b: Semantic Understanding (.harness())

The structural analysis tells us WHAT changed. The semantic analysis tells us WHY it matters:

- **PR narrative:** What is this PR actually trying to do? (Reads diff + PR description + commit messages)
- **Risk surface identification:** Which changes touch sensitive areas? (Auth boundaries, data handling, external APIs, configuration, infrastructure)
- **Unrelated change detection:** Are there changes in this PR that don't fit the narrative? (Common in large PRs — unrelated cleanup mixed with feature work)
- **Intent verification:** Does the diff actually accomplish what the PR description claims?

```python
class SemanticAnatomy(BaseModel):
    """Hybrid — structured fields for routing, string fields for LLM context."""
    pr_narrative: str           # Natural language: "what this PR does and why"
    risk_surfaces: list[str]    # Semantic risk areas identified
    unrelated_changes: list[str]  # Files that don't fit the PR story
    intent_gaps: list[str]      # Claimed in description but not in diff (or vice versa)
    context_notes: str          # Additional context for downstream reviewers (string for LLM consumption)
```

**Data flow:** Both sub-outputs combine into an `AnatomyResult` that the planner consumes.

---

## Phase 3: Planning — The Key Innovation

**Primitive:** `.harness()` (meta-prompting)
**Purpose:** Dynamically determine WHAT to review and craft specific reviewer prompts.

This is the most important phase in the pipeline. The planner does NOT select from a fixed menu of review types. It REASONS about what aspects of this specific PR need review and generates targeted investigation prompts for each.

### How the Planner Works

1. **Reads** the intake classification + full anatomy output
2. **Reasons** for each change cluster: "What could go wrong here? What expertise is needed? What non-obvious interactions should be checked?"
3. **Generates** a list of `ReviewDimension` objects, each containing a dynamically crafted prompt for a reviewer agent

### The Meta-Prompting Pattern

The planner is a `.harness()` that spawns reviewers by crafting their prompts at runtime. This is the Contract-AF meta-prompting pattern applied to PR review:

```python
# The planner doesn't select from a fixed menu.
# It GENERATES the review strategy from the content.

# Example: PR adds a new payment processing endpoint
planner_output = ReviewPlan(
    dimensions=[
        ReviewDimension(
            name="Payment Input Validation",
            review_prompt="""You are reviewing a new payment endpoint for input
            validation completeness. The endpoint accepts credit card data via
            POST /api/payments. Check:
            - Are all input fields validated (card number format, expiry, CVV)?
            - Is there SQL injection protection on the query in line 47?
            - Are monetary amounts validated (no negative values, overflow)?
            Focus on: src/api/payments.py (lines 30-95), src/validators/payment.py
            Context: The existing validation in src/validators/base.py uses Pydantic —
            check if the new endpoint follows the same pattern.""",
            target_files=["src/api/payments.py", "src/validators/payment.py"],
            context_files=["src/validators/base.py"],
            priority=1,
        ),
        ReviewDimension(
            name="Transaction State Consistency",
            review_prompt="""You are reviewing a payment processing flow for state
            consistency under failure. The new endpoint creates a payment record,
            charges the card via Stripe API, then updates the record. Check:
            - What happens if the Stripe call fails after the record is created?
            - Is there a transaction/rollback mechanism?
            - Are there race conditions if two requests hit simultaneously?
            Focus on: src/services/payment_service.py (lines 15-80)
            Context: Read src/services/order_service.py for how existing flows handle
            similar state transitions.""",
            target_files=["src/services/payment_service.py"],
            context_files=["src/services/order_service.py"],
            priority=1,
        ),
        # ... more dimensions crafted from the PR content
    ]
)
```

### Why This Matters

A PR modifying database migrations gets reviewers for "schema compatibility", "rollback safety", "data integrity" — not "security" and "performance" from a generic checklist.

A PR refactoring logging across 20 files gets reviewers for "behavioral preservation", "consistency", "missed files" — dimensions that would never appear in a static taxonomy.

The investigation path emerges from the content of the PR, not from a fixed configuration. Novel PRs get novel review strategies.

### Review Dimension Schema

```python
class ReviewDimension(BaseModel):
    """Each dimension becomes one parallel reviewer instance."""
    id: str               # Unique identifier
    name: str             # Human-readable name (for comments attribution)
    review_prompt: str    # THE dynamically crafted prompt (string — consumed by LLM)
    target_files: list[str]   # Files this reviewer must examine
    context_files: list[str]  # Additional files for reference (blast radius, imports)
    priority: int         # Higher = more important = gets budget first
    budget: BudgetAllocation  # Cost/time cap for this dimension

class ReviewPlan(BaseModel):
    """The planner's complete output."""
    dimensions: list[ReviewDimension]  # What to review
    cross_ref_hints: list[str]        # Suspected interactions for Phase 5 (string for LLM)
    ai_adjusted: bool                 # Whether plan was adjusted for AI-generated code
    total_budget: BudgetAllocation
```

### Budget Awareness

The planner is budget-aware. For a `quick` review depth, it generates 2-3 high-priority dimensions. For `deep`, it might generate 8-12. The planner receives the budget allocation from config and distributes it across dimensions by priority.

---

## Phase 4: Parallel Review

**Primitive:** `.harness()` × N (one per review dimension), streaming output
**Purpose:** Execute the review plan — each dimension runs as an independent agent.

### One Agent, N Prompts

There is ONE reviewer agent definition. The planner creates N instances by passing N different prompts. This is the architectural adaptability — no hardcoded reviewer types, no static dispatch.

Each reviewer instance:
- Receives its **dynamically crafted prompt** from the planner
- Has tool access to read the **target files** and **context files**
- Can **follow references** (up to 3 hops) when it discovers a relevant connection
- Can **self-escalate** by spawning a child harness for deep investigation (up to 2 children)
- **Emits findings** to a shared `asyncio.Queue` as it works (streaming to Phase 5)

### Inner Loop (Per-Reviewer Adaptation)

Each reviewer has bounded autonomy:

| Mechanism | Cap | Trigger |
|---|---|---|
| Reference following | 3 hops | Found an import/call that's relevant |
| Child harness spawning | 2 children | Critical signal needs deeper investigation |
| Early exit | - | No issues found in target files → stop early |

The child harness spawning is the inner loop's meta-prompting: a reviewer discovers something that needs deeper investigation and crafts a specific prompt for a child agent. For example, a reviewer checking error handling might discover that the error class hierarchy is unusual and spawn a child to investigate the base error class.

### Finding Schema

```python
class ReviewFinding(BaseModel):
    """Emitted to the findings queue as reviewers work."""
    dimension_id: str     # Which review dimension produced this
    dimension_name: str   # Human-readable (for comment attribution)
    file_path: str        # Path relative to repo root
    line_start: int       # Start line in the diff
    line_end: int         # End line in the diff
    hunk_context: str     # The code context around the finding
    severity: str         # critical | important | suggestion | nitpick
    title: str            # Concise title for the comment
    body: str             # Detailed explanation
    suggestion: str | None  # Concrete fix (code block) if applicable
    evidence: str         # Code references that support this finding
    confidence: float     # 0.0-1.0
    tags: list[str]       # Machine-readable category tags
```

### Concurrency Control

Reviewers run with controlled concurrency:

```python
semaphore = asyncio.Semaphore(config.max_concurrent_reviewers)  # default: 8

async def run_reviewer(dimension: ReviewDimension, queue: asyncio.Queue):
    async with semaphore:
        findings = await app.harness(
            prompt=dimension.review_prompt,
            schema=ReviewFindings,
            cwd=repo_path,
        )
        await queue.put(findings)

# All reviewers launched concurrently, semaphore controls parallelism
tasks = [run_reviewer(dim, findings_queue) for dim in plan.dimensions]
await asyncio.gather(*tasks)
await findings_queue.put(None)  # Sentinel: all reviewers done
```

---

## Phase 5: Review Layer (Streaming)

Three agents run in parallel, consuming findings from the queue as Phase 4 reviewers produce them:

### Cross-Reference Resolver (.harness())

The most valuable part of the pipeline. Watches for **interactions between findings from different reviewers** that individual reviewers couldn't see.

**What it looks for:**
- **Compound risks:** Reviewer A flags a missing null check, Reviewer B flags a path that passes null — these combine into a crash scenario
- **Assumption violations:** Change A modifies a function's behavior, Change B relies on the old behavior
- **Consistency gaps:** Change A uses pattern X, Change B uses pattern Y for the same thing
- **Transitive effects:** Change A → affects Module B (blast radius) → affects what Change C does in Module B

**How it works:**
1. Maintains a running set of all findings received so far
2. For each new finding, checks it against all previous findings
3. Uses the planner's `cross_ref_hints` as starting points for investigation
4. Can spawn up to 5 targeted deep-dive harnesses for suspicious combinations

**Middle loop budget:** Max 5 cross-ref deep-dives per pipeline run.

```
Time →

Reviewer A:  [========================]
Reviewer B:      [====================]
Reviewer C:          [================]

Cross-Ref:          [========================]  (starts when first findings arrive)
Adversary:          [========================]  (starts when first findings arrive)
Coverage:                               [====]  (checks after most findings in)
```

### Adversary Reviewer (.harness())

Challenges findings and hunts for what was missed. Explicitly incentivized to:

1. **Identify false positives:** Is this finding about a pre-existing issue, not something introduced by the PR? Is the flagged pattern actually the project's established convention?
2. **Downweight noise:** Is this a real issue or stylistic preference? Is the severity overstated?
3. **Hunt hidden traps:** What did ALL the reviewers miss? What issues exist in the interaction between changed and unchanged code that no individual reviewer could see?
4. **AI-code skepticism:** If `intake.ai_generated > 0.5`, apply additional scrutiny:
   - Do imported modules/functions actually exist?
   - Are there over-abstractions that add complexity without value?
   - Do tests assert meaningful things (not just exist)?
   - Is the code logically correct but architecturally wrong?

The adversary's output feeds directly into scoring:
- Findings confirmed by adversary → severity boost
- Findings challenged by adversary → severity discount
- Hidden traps found by adversary → new findings at appropriate severity

### Coverage Gate (.ai())

After most findings are in, the coverage gate checks completeness:

- Were all change clusters reviewed by at least one dimension?
- Are there blast radius files with significant dependency exposure that no reviewer examined?
- Does the review plan have obvious gaps given the PR type? (e.g., a feature PR with no test adequacy review)

If gaps are found, it spawns **gap reviewers** (Phase 4 agents with new prompts crafted for the uncovered areas). Gap findings flow back into cross-ref + adversary.

**Outer loop budget:** Max 2 coverage iterations.

---

## Phase 6: Synthesis (Deterministic Code)

**Primitive:** Code (NOT LLM)
**Purpose:** Score, rank, deduplicate, and format findings.

All done programmatically:

### Scoring

```python
BASE_WEIGHTS = {
    "critical": 1.0,
    "important": 0.7,
    "suggestion": 0.3,
    "nitpick": 0.1,
}

MULTIPLIERS = {
    "cross_ref_compound": 1.5,    # Cross-ref found compound risk
    "adversary_confirmed": 1.3,   # Adversary confirmed exploitation scenario
    "adversary_challenged": 0.5,  # Adversary successfully challenged
    "ai_generated_pr": 1.2,       # Extra weight for AI-generated PRs (higher noise baseline)
    "blast_radius_high": 1.2,     # Change affects many files
}

def compute_score(finding: ScoredFinding) -> float:
    base = BASE_WEIGHTS[finding.severity]
    score = base * finding.confidence
    for multiplier_key in finding.active_multipliers:
        score *= MULTIPLIERS[multiplier_key]
    return round(score, 3)
```

### Deduplication

- Exact dedup: same file + same line range + same category → merge
- Near-dedup: different reviewers found the same issue from different angles → merge, keep the better-explained version
- Dedup uses code (not LLM) for exact matches; `.ai()` gate for near-matches when descriptions differ

### Line Mapping

Map finding line numbers to the PR diff coordinate system. GitHub expects line numbers relative to the diff, not absolute file positions. This is a programmatic transformation using the parsed diff structure from Phase 2.

### Filtering

Apply confidence thresholds:
- `critical` / `important`: keep if confidence ≥ 0.3
- `suggestion`: keep if confidence ≥ 0.5
- `nitpick`: keep if confidence ≥ 0.7

### Output

```python
class SynthesisResult(BaseModel):
    findings: list[ScoredFinding]  # Sorted by composite score descending
    summary: ReviewSummary         # Aggregate stats
    review_event: str              # APPROVE | COMMENT | REQUEST_CHANGES
```

**Review event logic (code):**
- Any `critical` findings → `REQUEST_CHANGES`
- `important` findings but no `critical` → `COMMENT`
- Only `suggestion` / `nitpick` → `APPROVE` (with comments)
- Nothing found → `APPROVE` (clean)

---

## Phase 7: Output

**Primitive:** Code (GitHub API)
**Purpose:** Post the review to GitHub and emit structured output.

### GitHub PR Review API

```python
# Single API call creates review with all inline comments
POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews
{
    "body": "<executive summary markdown>",
    "event": "COMMENT",  # or APPROVE / REQUEST_CHANGES
    "comments": [
        {
            "path": "src/api/payments.py",
            "line": 42,
            "side": "RIGHT",
            "body": "### ⚠️ Missing input validation\n\nThe `amount` field is passed directly..."
        }
    ]
}
```

### Comment Formatting

Each inline comment follows a consistent format:

```markdown
### {severity_emoji} {title}

{body}

{suggestion_block if applicable}

---
<sub>Found by: {dimension_name} · Confidence: {confidence} · {category}</sub>
```

Severity emojis:
- 🔴 critical
- 🟠 important
- 🔵 suggestion
- ⚪ nitpick

### Output Modes

| Mode | Description | Use Case |
|---|---|---|
| GitHub PR Review | Inline comments + summary | Primary output |
| Structured JSON | Full findings with metadata | CI/CD integration |
| SARIF | Static analysis format | GitHub Security tab |
| Markdown | Standalone report | Email, Slack, CLI |

---

## Three Nested Control Loops

| Loop | Scope | Trigger | Budget |
|---|---|---|---|
| **Inner** | Per-reviewer adaptation | Found reference / critical signal | Max 3 hops, 2 child spawns |
| **Middle** | Cross-agent deep-dives | Compound risk / interaction detected | Max 5 cross-ref deep-dives |
| **Outer** | Pipeline coverage | Gap in review coverage | Max 2 iterations |

Each loop has hard caps. Without caps, adaptive systems become unbounded cost sinks.

---

## AI-PR Handling

When the intake classifier detects signals of AI-generated code (`ai_generated > 0.5`), this doesn't trigger a separate review pass. Instead, it adjusts the review plan across ALL dimensions.

### Detection Signals

The intake classifier looks for (programmatic checks, not LLM):
- **Naming patterns:** Over-descriptive variable names (`descriptive_variable_name_for_the_user_input`)
- **Comment density:** Comments on obvious code, docstrings on trivial functions
- **Structural uniformity:** All functions roughly same length, same pattern
- **Import patterns:** Unusual or non-existent packages imported
- **Test patterns:** Tests that mirror the implementation too closely (testing the same logic, not the behavior)

### Plan Adjustments

When `ai_generated > 0.5`, the planner:
1. Adds a "hallucination check" dimension — verify that all referenced APIs, modules, and functions actually exist
2. Adjusts existing dimensions to include over-abstraction detection
3. Increases scrutiny on test adequacy — AI-generated tests often test the wrong thing
4. Applies the `ai_generated_pr` scoring multiplier (1.2x) to all findings

This is NOT a separate pipeline. It's the planner adapting its strategy — the same dynamic meta-prompting mechanism used for all PRs, just with additional context.

---

## Budget Management

```python
class BudgetConfig(BaseModel):
    """All behavioral tuning in one place."""
    # Global caps
    max_cost_usd: float = 2.0
    max_duration_seconds: int = 300  # 5 minutes

    # Phase-level
    max_concurrent_reviewers: int = 8
    phase_budgets: dict[str, float] = {
        "intake": 0.05,
        "anatomy": 0.15,
        "planning": 0.15,
        "review": 0.90,    # Most budget goes here
        "cross_ref": 0.30,
        "adversary": 0.25,
        "coverage": 0.10,
        "synthesis": 0.00,  # Code, no LLM cost
        "output": 0.00,     # Code, no LLM cost
    }

    # Loop caps
    max_reference_follows_per_reviewer: int = 3
    max_child_spawns_per_reviewer: int = 2
    max_cross_ref_deep_dives: int = 5
    max_coverage_iterations: int = 2

    # Model routing
    models: dict[str, str] = {
        "intake_gate": "budget",       # .ai() fast classification
        "anatomy_semantic": "mid",     # Narrative understanding
        "planner": "premium",          # Critical: quality of plan = quality of review
        "reviewer": "premium",         # Deep code analysis
        "cross_ref": "premium",        # Interaction detection needs best reasoning
        "adversary": "premium",        # Challenging findings needs strong reasoning
        "coverage_gate": "budget",     # Simple completeness check
        "dedup_gate": "budget",        # Near-duplicate detection
    }
```

**Model routing philosophy:** Budget models for gates and classification. Premium models for the planner (plan quality determines review quality), reviewers (deep code reasoning), cross-ref resolver (interaction detection), and adversary (challenge quality).

---

## `.ai()` vs `.harness()` Assignment

Following the decision tree from [CLAUDE.md](../../CLAUDE.md):

| Agent | Primitive | Why |
|---|---|---|
| Intake classifier | `.ai()` + fallback | Fast classification, < 500 tokens, flat schema |
| Structural analysis | **Code** | Deterministic computation |
| Semantic anatomy | `.harness()` | Navigates diff, multi-turn, rich output |
| Planner | `.harness()` | Meta-prompting: crafts child prompts, reads full anatomy |
| Reviewer (×N) | `.harness()` | Navigates code, follows references, spawns children |
| Cross-ref resolver | `.harness()` | Reasons over all findings, spawns deep-dives |
| Adversary reviewer | `.harness()` | Challenges findings, hunts hidden traps |
| Coverage gate | `.ai()` | Simple completeness check, flat schema |
| Dedup gate | `.ai()` | Near-duplicate classification, 3 fields |
| Scoring | **Code** | Deterministic formula |
| Line mapping | **Code** | Deterministic transformation |
| Comment formatting | **Code** | Template-based |
| GitHub posting | **Code** | API call |

**Every `.ai()` call has a fallback.** If the intake classifier isn't confident, it escalates to `.harness()`. If the dedup gate isn't confident, findings are kept (err on the side of reporting).

---

## Inter-Agent Data Flow (Archei Rules)

| Edge | Format | Why |
|---|---|---|
| Intake → Planner | **Structured JSON** | Code routes based on `pr_type`, `complexity` |
| Anatomy → Planner | **Hybrid** | Structured clusters for routing + string narrative for LLM context |
| Planner → Reviewers | **String** (review_prompt) | LLM consumes the dynamically crafted prompt |
| Reviewers → Queue | **Structured JSON** | Code deduplicates, scores, maps lines |
| Queue → Cross-Ref | **String** (finding descriptions) | LLM reasons about interactions |
| Queue → Adversary | **String** (finding descriptions) | LLM challenges and hunts |
| Adversary → Scoring | **Structured JSON** | Code applies multipliers |
| Scoring → Output | **Structured JSON** | Code formats comments and calls API |

---

## Comparison with Reference Architectures

| Pattern | SEC-AF | Contract-AF | PR-AF |
|---|---|---|---|
| Streaming pipeline | HUNT → PROVE queue | Analysts → Review Layer queue | Reviewers → Cross-Ref/Adversary queue |
| Adversarial tension | Hunters → Provers | Analysts → Adversary | Reviewers → Adversary |
| Meta-prompting | Strategy selection | Clause analysts spawn children | **Planner generates all reviewer prompts** |
| Dynamic depth | Depth profiles | Inner/Middle/Outer loops | Inner/Middle/Outer loops |
| .ai() gates | Severity, dedup, strategy | Intake, coverage | Intake, coverage, dedup |
| Deterministic scoring | Exploitability scores | Severity × multipliers | Severity × multipliers |
| Blast radius | `diff_analysis.py` | Cross-ref tracing | `blast_radius.py` |
| Budget management | Per-phase cost caps | Per-loop budget caps | Both |

**PR-AF's unique contribution:** The planner is a full `.harness()` that does meta-prompting at the PLAN level. In Contract-AF, the planner routes sections to fixed analyst types, and meta-prompting happens WITHIN analysts. In PR-AF, the planner itself generates the entire review strategy — there are no fixed reviewer types at all.

---

## Source Code Layout

```
src/pr_af/
├── app.py                 # FastAPI application, /review endpoint
├── config.py              # Configuration: models, budgets, caps, comment format
├── orchestrator.py        # Pipeline orchestrator (phases 1-7)
├── diff_engine.py         # Programmatic diff parsing (code, not LLM)
├── blast_radius.py        # Dependency graph + blast radius computation (code)
├── scoring.py             # Deterministic scoring engine
├── agents/
│   ├── intake.py          # PR classification (.ai() + .harness() fallback)
│   ├── anatomy.py         # Semantic understanding (.harness())
│   ├── planner.py         # Dynamic review planning (meta-prompting .harness())
│   ├── reviewer.py        # Generic reviewer (prompt-driven .harness())
│   ├── cross_ref.py       # Cross-reference interaction detection (.harness())
│   ├── adversary.py       # Adversarial challenge (.harness())
│   ├── coverage.py        # Coverage gate (.ai())
│   └── gap_reviewer.py    # Gap analysis (reuses reviewer.py with gap prompt)
├── schemas/
│   ├── input.py           # ReviewInput, GitHub PR models
│   ├── gates.py           # .ai() gate schemas (flat, 2-4 fields)
│   ├── pipeline.py        # Inter-agent schemas (IntakeResult, AnatomyResult, etc.)
│   └── output.py          # ScoredFinding, ReviewSummary, GitHubComment
├── github/
│   ├── client.py          # GitHub API client (fetch PR data, post reviews)
│   ├── models.py          # GitHub data models (PR, File, Comment)
│   └── diff_parser.py     # Parse GitHub unified diff format
└── reasoners/
    └── harnesses.py       # AgentField agent definitions
```

**`config.py`** is where you tune the system. Model assignments per agent, budget caps per loop level, comment format templates, review strictness, custom ignore patterns. Most behavioral changes start here.

**`scoring.py`** is intentionally separate from agents so scoring logic can be tested, audited, and modified without touching agent code.

**`diff_engine.py`** and **`blast_radius.py`** are pure code — no LLM calls. They handle the programmatic work that should never be delegated to an LLM.

**`agents/reviewer.py`** is a single agent that takes a dynamically crafted prompt. There are no `security_reviewer.py`, `performance_reviewer.py`, etc. The review dimensions emerge from the planner.
