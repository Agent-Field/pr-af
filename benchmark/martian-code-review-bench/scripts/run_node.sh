#!/usr/bin/env bash
# Launch the PR-AF node with the whole pipeline pinned to GLM-5.2 via OpenRouter.
# Harness (opencode) needs the `openrouter/` prefix; .ai() (direct OpenRouter API) needs the bare slug.
set -euo pipefail
# scripts/ -> martian-code-review-bench -> benchmark -> pr-af repo root
cd "$(dirname "$0")/../../.."

# Load OPENROUTER_API_KEY etc. from .env, then override the model + budget knobs.
set -a
# shellcheck disable=SC1091
source .env
set +a

export NODE_ID=pr-af
export AGENTFIELD_SERVER="${AGENTFIELD_SERVER:-http://localhost:8080}"
export AGENT_CALLBACK_URL="${AGENT_CALLBACK_URL:-http://127.0.0.1:8004}"

# --- the experiment: GLM-5.2 everywhere ---
export PR_AF_PROVIDER=opencode
export PR_AF_MODEL=openrouter/z-ai/glm-5.2     # .harness() -> opencode -m
export PR_AF_AI_MODEL=openrouter/z-ai/glm-5.2  # .ai()      -> litellm (needs openrouter/ prefix too)
export PR_AF_MAX_TURNS=60

# Generous budget for the hardest Martian-bench PR (keycloak/keycloak#32918).
export PR_AF_MAX_COST_USD=8.0
export PR_AF_MAX_DURATION_SECONDS=2400

export PR_AF_WORKDIR="${PR_AF_WORKDIR:-/tmp/pr-af-work}"

# --- opencode execution efficiency / parallelism (speed levers, zero quality cost) ---
# Reuse one data dir so the per-call SQLite migration runs ONCE per process, not per
# harness call (~35 calls/review each paid the migration before).
export AGENTFIELD_OPENCODE_REUSE_DATA_DIR=true
# Raise the harness concurrency ceiling (default 10) so parallel harness calls
# don't queue during a deep review. Cost is no object here.
export OPENCODE_MAX_CONCURRENT="${OPENCODE_MAX_CONCURRENT:-24}"

# Authenticated GitHub token (from gh CLI) so fetch_pr + clone aren't capped at
# the 60 req/hr unauthenticated limit during a 38-PR campaign.
if [ -z "${GH_TOKEN:-}" ]; then
  export GH_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
[ -n "${GH_TOKEN:-}" ] && echo "[run_node] GH_TOKEN set (authenticated GitHub API)" || echo "[run_node] WARNING: no GH_TOKEN"

# Force HITL OFF (no human approval gate) — we want a direct dry-run.
unset HAX_API_KEY || true

echo "[run_node] PR_AF_MODEL=$PR_AF_MODEL  PR_AF_AI_MODEL=$PR_AF_AI_MODEL"
echo "[run_node] server=$AGENTFIELD_SERVER  budget=\$$PR_AF_MAX_COST_USD / ${PR_AF_MAX_DURATION_SECONDS}s"
exec uv run python main.py
