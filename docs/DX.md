# PR-AF Developer Experience

How to invoke PR-AF, what inputs it accepts, what outputs it produces, and how to integrate it into CI/CD pipelines.

---

## Input Modes

PR-AF accepts reviews through three input modes, each with different context richness and speed tradeoffs.

### Mode 1: GitHub PR URL (Full Context)

```json
POST /api/v1/execute/async/pr-af.review
{
  "input": {
    "pr_url": "https://github.com/owner/repo/pull/123",
    "depth": "auto",
    "max_cost_usd": 2.00
  }
}
```

**What's fetched via GitHub API:**
- PR metadata: title, description, labels, linked issues, author, reviewers
- Commit messages and commit SHAs (base + head)
- Diff: unified diff format with all changed files
- Full file contents at both base and head commits
- Repository file tree (for blast radius computation)

**What's cloned (optional, for deep review):**
- Full repository at HEAD for `.harness()` agents to navigate freely
- Configurable: `--clone` (full clone), `--shallow` (depth=1), `--no-clone` (API only)

**Authentication:**
- `GITHUB_TOKEN` env var (personal access token or GitHub App installation token)
- GitHub App: preferred for CI/CD (fine-grained permissions, higher rate limits)

**Best for:** CI/CD pipelines, GitHub Action triggers, full-featured review.

### Mode 2: Diff Only (Lightweight)

```json
POST /api/v1/execute/async/pr-af.review
{
  "input": {
    "diff": "--- a/file.py\n+++ b/file.py\n@@ -1,3 +1,4 @@\n...",
    "depth": "quick",
    "max_cost_usd": 0.50
  }
}
```

**What's available:**
- The raw unified diff only
- No repo context, no PR metadata, no blast radius
- Limited to what's visible in the diff hunks

**Limitations:**
- Can't follow references or check blast radius
- Can't verify imports or check existing code patterns
- Planner generates fewer dimensions (less context to reason from)
- No GitHub comment output (no PR to post to)

**Best for:** Pre-commit hooks, local development, quick sanity checks, non-GitHub repos.

### Mode 3: Local Repo + Branch

```json
POST /api/v1/execute/async/pr-af.review
{
  "input": {
    "repo_path": "/path/to/repo",
    "base_ref": "main",
    "head_ref": "feature-branch",
    "depth": "standard",
    "max_cost_usd": 1.50
  }
}
```

**What's available:**
- Full repository on disk (fastest — no cloning needed)
- Git diff computed locally between base and head
- Full file contents at both commits
- Complete blast radius analysis

**Output options:** Markdown to stdout, JSON file, or post to GitHub if `--pr <number>` is provided.

**Best for:** Local development, self-hosted Git, testing the review before pushing.

---

## CI/CD Integration

### GitHub Action (Primary)

```yaml
name: PR Review
on:
  pull_request:
    types: [opened, synchronize, reopened]

jobs:
  review:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write
      contents: read
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0  # Full history for diff analysis

      - uses: agentfield/pr-af-action@v1
        with:
          # Required
          github-token: ${{ secrets.GITHUB_TOKEN }}

          # Review depth (default: auto)
          # auto: determines from PR size (small→quick, large→deep)
          # quick: 2-3 dimensions, budget models, fast
          # standard: 4-6 dimensions, mid-tier models
          # deep: 8-12 dimensions, premium models, full coverage
          depth: auto

          # Focus areas (default: auto)
          # auto: planner decides based on PR content
          # Or specify: security, correctness, performance, tests, style
          focus: auto

          # Budget caps
          max-cost: "2.00"        # USD
          max-duration: "300"     # seconds

          # Comment behavior
          comment-mode: inline    # inline | summary-only | both
          review-event: auto      # auto | comment | approve | request-changes

          # Ignore patterns (glob)
          ignore-paths: |
            docs/**
            *.md
            .github/**

          # AgentField configuration
          agentfield-api-key: ${{ secrets.AGENTFIELD_API_KEY }}
          model-tier: standard    # budget | standard | premium

        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
```

### GitHub App (Enterprise)

For organizations that want a persistent bot reviewer:

1. Install the PR-AF GitHub App on your org/repo
2. Configure via `.pr-af.yml` in repo root (see [Configuration](#configuration))
3. PR-AF automatically reviews all PRs that match the configured rules
4. Posts reviews as `pr-af[bot]` with configurable avatar

### Generic CI/CD (GitLab, Bitbucket, Jenkins)

```bash
# Call the API endpoint with curl or httpx
curl -X POST https://agentfield.example.com/api/v1/execute/async/pr-af.review \
  -H "Authorization: Bearer $AGENTFIELD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "diff": "'"$(git diff $CI_MERGE_REQUEST_DIFF_BASE_SHA...$CI_COMMIT_SHA)"'",
      "depth": "standard",
      "max_cost_usd": 2.00
    }
  }' \
  -o review.json

# Post-process the JSON output with your platform's comment API
```

### Webhook Endpoint

For event-driven architectures:

```bash
# Start the PR-AF server
pr-af serve --port 8080

# Configure GitHub webhook:
# URL: https://your-server/webhook
# Events: Pull requests
# Content type: application/json
```

The server accepts GitHub webhook payloads and processes reviews asynchronously. Status is available via `/jobs/{id}`.

---

## Output Formats

### 1. GitHub PR Review (Primary)

A single GitHub review with inline comments:

```
Review Summary:
Found 5 issues: 1 critical, 2 important, 2 suggestions

🔴 [Critical] SQL injection in user input handling
   src/api/users.py:42
   The raw query parameter is interpolated directly into the SQL query...

🟠 [Important] Missing error handling on payment callback
   src/services/payment.py:87
   If the Stripe webhook fails, the order status is never updated...

...
```

Each inline comment:
```markdown
### 🟠 Missing null check before dereference

The `user.profile` access on line 42 can throw if `user` is null,
which happens when the auth middleware passes through anonymous requests
(see `src/middleware/auth.py:28`).

```suggestion
if user is None:
    raise HTTPException(status_code=401, detail="Authentication required")
profile = user.profile
```

---
<sub>Found by: Authorization Completeness · Confidence: 0.85 · correctness</sub>
```

### 2. Structured JSON

```json
{
  "review_id": "rev_abc123",
  "pr_url": "https://github.com/owner/repo/pull/123",
  "review_event": "COMMENT",
  "summary": {
    "total_findings": 5,
    "by_severity": {"critical": 1, "important": 2, "suggestion": 2},
    "review_dimensions": 4,
    "ai_generated_confidence": 0.1,
    "cost_usd": 1.23,
    "duration_seconds": 45
  },
  "findings": [
    {
      "id": "f_001",
      "dimension": "Input Validation",
      "file_path": "src/api/users.py",
      "line_start": 42,
      "line_end": 42,
      "severity": "critical",
      "confidence": 0.92,
      "title": "SQL injection in user input handling",
      "body": "The raw query parameter `user_id` is interpolated...",
      "suggestion": "Use parameterized queries: `cursor.execute('SELECT * FROM users WHERE id = %s', (user_id,))`",
      "evidence": "src/api/users.py:42 — `f\"SELECT * FROM users WHERE id = {user_id}\"`",
      "tags": ["security", "injection", "cwe-89"],
      "score": 1.196,
      "multipliers": ["adversary_confirmed"]
    }
  ],
  "metadata": {
    "intake": {"pr_type": "feature", "complexity": "standard"},
    "anatomy": {"clusters": 3, "blast_radius_files": 12},
    "plan": {"dimensions": 4, "ai_adjusted": false},
    "budget": {"spent_usd": 1.23, "cap_usd": 2.00}
  }
}
```

### 3. SARIF (Static Analysis Results Interchange Format)

For GitHub Security tab integration:

```json
{
  "$schema": "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json",
  "version": "2.1.0",
  "runs": [{
    "tool": {"driver": {"name": "PR-AF", "version": "1.0.0"}},
    "results": [{
      "ruleId": "pr-af/input-validation",
      "level": "error",
      "message": {"text": "SQL injection in user input handling"},
      "locations": [{
        "physicalLocation": {
          "artifactLocation": {"uri": "src/api/users.py"},
          "region": {"startLine": 42}
        }
      }]
    }]
  }]
}
```

### 4. Markdown (Standalone)

```bash
pr-af review --output markdown --output-file review.md
```

Full report with executive summary, findings by severity, and recommendations.

---

## Configuration

### Repo-Level Config (`.pr-af.yml`)

```yaml
# .pr-af.yml — checked into repo root

# Review depth
depth: auto              # auto | quick | standard | deep

# Budget
budget:
  max_cost_usd: 2.00
  max_duration_seconds: 300
  model_tier: standard   # budget | standard | premium

# Ignore patterns (glob)
ignore:
  - "docs/**"
  - "*.md"
  - ".github/**"
  - "vendor/**"
  - "node_modules/**"
  - "**/*.generated.*"

# Auto-depth rules (override 'auto' behavior)
depth_rules:
  - match: "src/auth/**"
    depth: deep           # Always deep-review auth changes
  - match: "migrations/**"
    depth: deep           # Always deep-review schema changes
  - match: "scripts/**"
    depth: quick          # Light review for scripts
  - match_label: "hotfix"
    depth: deep           # Hotfixes get deep review

# Custom review hints
# These are passed to the planner as additional context
# (not hardcoded rules — the planner decides how to use them)
hints:
  - "This project uses SQLAlchemy ORM — raw SQL queries are a code smell"
  - "All API endpoints must use Pydantic models for request/response validation"
  - "Payment-related code must handle idempotency"

# Comment preferences
comments:
  min_severity: suggestion  # Only post suggestion+ (skip nitpicks)
  max_comments: 20          # Cap inline comments to avoid overwhelming
  include_suggestions: true # Include ```suggestion blocks
  group_by_file: false      # Or group related findings
```

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `GITHUB_TOKEN` | Yes (for GitHub output) | GitHub PAT or App installation token |
| `AGENTFIELD_API_KEY` | Yes | AgentField platform API key |
| `OPENROUTER_API_KEY` | Yes | LLM provider API key |
| `PR_AF_CONFIG` | No | Path to config file (default: `.pr-af.yml`) |
| `PR_AF_DEPTH` | No | Override review depth |
| `PR_AF_MAX_COST` | No | Override max cost |

---

## Cost Estimates

Cost depends on PR size and review depth. These assume standard complexity.

| PR Size | Quick | Standard | Deep |
|---|---|---|---|
| Small (< 100 lines, 1-3 files) | ~$0.10-$0.20 | ~$0.30-$0.60 | ~$0.80-$1.50 |
| Medium (100-500 lines, 4-10 files) | ~$0.20-$0.40 | ~$0.60-$1.20 | ~$1.50-$3.00 |
| Large (500-2000 lines, 10-30 files) | ~$0.40-$0.80 | ~$1.00-$2.00 | ~$3.00-$6.00 |
| Massive (2000+ lines, 30+ files) | ~$0.60-$1.20 | ~$1.50-$3.00 | ~$5.00-$10.00 |

The `auto` depth setting uses PR size to select: small → quick, medium → standard, large+ → deep.

**Budget caps prevent runaway costs.** If a review hits the cost cap, it stops gracefully: posts whatever findings it has, notes the early termination in the summary, and reports partial coverage.
