#!/usr/bin/env bash
# run.sh — one-command E2E harness for the PR-AF Go port.
#
# Runs pr-af-go.review over a LOCAL fixture git repo (a seeded 2-commit diff) with
# the opencode HARNESS fully mocked, so the deterministic bulk of the review
# pipeline (anatomy -> meta selectors -> review dimensions -> evidence ->
# adversary -> compound/obligations -> synthesis -> output) runs with ZERO LLM
# calls and ZERO GitHub writes. It asserts the review succeeded, produced findings,
# has a non-empty review body, and that the expected phases fired (from the mock's
# PR_AF_MOCK_STATE_DIR/invocations.jsonl).
#
# ─────────────────────────────────────────────────────────────────────────────
# REQUIREMENTS / HONEST LIMITATIONS — read before running:
#
#   1. `af` CLI on PATH. This script starts a real control plane with
#      `af server`. If `af` is missing the script errors with install guidance and
#      exits 1 (it does NOT try to fake a control plane).
#
#   2. OPENROUTER_API_KEY must be set. PR-AF has four `.ai()` touchpoints — the
#      intake gate, the coverage gate, the merge-blocker gate, and comment polish —
#      that call agent.AI() -> OpenRouter, NOT the opencode harness, so the mock
#      CLI never intercepts them. The committed node builder (internal/node/
#      node.go BuildAgent) HARDWIRES the AI base URL to https://openrouter.ai and
#      only test files are in this task's scope, so those calls cannot be
#      redirected to a local stub from here. The intake gate error is FATAL
#      (orchestrator.Run returns it), so without a key the review cannot complete.
#      => A fully-offline run would need a one-line BuildAgent change to honor an
#         AI base-URL env (e.g. OPENROUTER_BASE_URL); until then this harness is
#         zero-LLM on the HARNESS path and makes a handful of cheap classification
#         calls on the .ai() path. If OPENROUTER_API_KEY is unset the script SKIPS
#         cleanly (exit 0) with this explanation.
#
#   3. dry_run=true. The review is posted to NOTHING: the input carries repo_path
#      (no pr_url), so there is no PR to post to, and dry_run keeps the HITL gate
#      and GitHub PostReview off. The GitHub posting path is covered by the unit
#      tests (internal/github, internal/orch/output_test.go); this harness proves
#      the pipeline end to end, not the REST post.
#
# Usage:  ./run.sh [--keep]
#   --keep   leave the control plane and node running for UI inspection at
#            http://localhost:18080 (default stops the node, leaves the CP up).
set -uo pipefail

# ---------------------------------------------------------------------------
# Paths / config
# ---------------------------------------------------------------------------
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_ROOT="$(cd "$HERE/../.." && pwd)"          # .../pr-af/go
TS="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="$HERE/runs/$TS"
SHIM="$RUN_DIR/shim"
STATE="$RUN_DIR/state"
FIXTURE="$RUN_DIR/fixture-repo"
CP_PORT=18080
NODE_PORT=18017
CP_URL="http://localhost:$CP_PORT"
NODE_URL="http://localhost:$NODE_PORT"
NODE_ID="pr-af-go"
KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

log()  { printf '\033[1;36m[e2e]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m[FAIL]\033[0m %s\n' "$*" >&2; }

NODE_PID=""
ASSERT_FAILS=0
assert() { # assert <condition-exit-code-in-$1> <desc>
  if [[ "$1" -eq 0 ]]; then ok "$2"; else err "$2"; ASSERT_FAILS=$((ASSERT_FAILS+1)); fi
}

cleanup() {
  [[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null
  pkill -f "e2e/runs/.*/[p]r-af" 2>/dev/null
  if [[ "$KEEP" -eq 1 ]]; then
    log "--keep: leaving control plane ($CP_URL) and node up for inspection"
  else
    log "node stopped; control plane left running at $CP_URL"
  fi
}

# ---------------------------------------------------------------------------
# 0. Preconditions
# ---------------------------------------------------------------------------
for bin in go git curl python3; do
  command -v "$bin" >/dev/null 2>&1 || { err "'$bin' is required but not on PATH"; exit 1; }
done

if ! command -v af >/dev/null 2>&1; then
  err "'af' (AgentField CLI) is not on PATH — it is needed to start a local control plane."
  err "Install it (e.g. 'curl -fsSL https://get.agentfield.dev | sh' or per the AgentField"
  err "docs), ensure 'af server' works, then re-run. This harness will not fake a CP."
  exit 1
fi

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  log "SKIP: OPENROUTER_API_KEY is not set."
  log "PR-AF's intake/coverage/merge/polish .ai() gates call OpenRouter directly (the"
  log "committed BuildAgent hardwires the AI base URL), so they cannot be mocked from"
  log "this test-only harness. The intake gate error is fatal, so the review cannot"
  log "complete without a key. Set OPENROUTER_API_KEY and re-run for the full flow."
  log "(Everything on the opencode HARNESS path is still mocked — zero LLM there.)"
  exit 0
fi

# Past the guards: from here we start processes, so register cleanup.
trap cleanup EXIT
mkdir -p "$SHIM" "$STATE"

# ---------------------------------------------------------------------------
# 1. Build the opencode shim (literally named `opencode`) + the node binary
# ---------------------------------------------------------------------------
log "building mockcli -> $SHIM/opencode"
( cd "$GO_ROOT" && GOWORK=off go build -o "$SHIM/opencode" ./test/mockcli/ ) \
  || { err "mockcli build failed"; exit 1; }
log "building pr-af node -> $RUN_DIR/pr-af"
( cd "$GO_ROOT" && GOWORK=off go build -o "$RUN_DIR/pr-af" ./cmd/pr-af/ ) \
  || { err "pr-af build failed"; exit 1; }

# Materialize the scenario JSON in sync with the mock's baked default.
"$SHIM/opencode" -dump-scenario > "$RUN_DIR/scenario.json"
log "scenario -> $RUN_DIR/scenario.json"

# ---------------------------------------------------------------------------
# 2. Seed a local fixture git repo with a 2-commit diff.
#    computeRepoDiff() defaults to `HEAD~1...HEAD`, so the review sees commit 2.
# ---------------------------------------------------------------------------
log "seeding fixture repo $FIXTURE"
mkdir -p "$FIXTURE"
(
  cd "$FIXTURE"
  git init -q
  git config user.email pr-af-mock@example.com
  git config user.name  "PR-AF Mock"
  git config commit.gpgsign false
  printf 'def existing():\n    return 1\n' > base.py
  git add base.py
  git commit -q -m "base: seed commit"
  mkdir -p mockpkg
  cat > mockpkg/handler.py <<'PY'
def handle(request, retries=3):
    while True:
        try:
            return request.send()
        except TimeoutError:
            retries -= 1
PY
  git add mockpkg/handler.py
  git commit -q -m "feat: add request handler with retry loop"
)
ok "fixture repo has a 2-commit seeded diff (mockpkg/handler.py added)"

# ---------------------------------------------------------------------------
# 3. Control plane on :18080 (reuse if already listening)
# ---------------------------------------------------------------------------
if curl -sf --connect-timeout 3 -m 8 "$CP_URL/health" >/dev/null 2>&1; then
  log "control plane already up at $CP_URL — reusing"
else
  log "starting control plane: af server --port $CP_PORT"
  setsid nohup af server --port "$CP_PORT" --open=false > "$RUN_DIR/cp.log" 2>&1 &
  for _ in $(seq 1 60); do
    curl -sf --connect-timeout 3 -m 8 "$CP_URL/health" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -sf --connect-timeout 3 -m 8 "$CP_URL/health" >/dev/null 2>&1 \
    || { err "control plane did not come up (see $RUN_DIR/cp.log)"; exit 1; }
fi
ok "control plane healthy ($CP_URL)"

# ---------------------------------------------------------------------------
# 4. Start the pr-af-go node with the opencode shim wired in
#    (PR_AF_OPENCODE_BIN points the harness straight at the shim; PATH is belt
#    and suspenders). NODE_ID=pr-af-go opts into the Go sibling identity.
# ---------------------------------------------------------------------------
log "starting pr-af node on :$NODE_PORT (shim=$SHIM/opencode, cwd=$RUN_DIR)"
# cwd matters: harness calls made with an empty Cwd (intake fallback, dedup /
# worthiness gates) resolve their .agentfield_output.json RELATIVE to the node's
# cwd, so the node must run from a known-writable directory — the run dir.
cd "$RUN_DIR"
PATH="$SHIM:$PATH" \
  AGENTFIELD_SERVER="$CP_URL" \
  AGENT_CALLBACK_URL="$NODE_URL" \
  NODE_ID="$NODE_ID" \
  PORT="$NODE_PORT" \
  PR_AF_PROVIDER="opencode" \
  PR_AF_OPENCODE_BIN="$SHIM/opencode" \
  PR_AF_MOCK_STATE_DIR="$STATE" \
  PR_AF_MOCK_SCENARIO="$RUN_DIR/scenario.json" \
  OPENROUTER_API_KEY="$OPENROUTER_API_KEY" \
  GH_TOKEN="" \
  setsid nohup "$RUN_DIR/pr-af" > "$RUN_DIR/node.log" 2>&1 &
NODE_PID=$!
cd "$HERE"

for _ in $(seq 1 60); do
  curl -sf --connect-timeout 3 -m 8 "$NODE_URL/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf --connect-timeout 3 -m 8 "$NODE_URL/health" >/dev/null 2>&1 \
  || { err "node did not come up (see $RUN_DIR/node.log)"; exit 1; }
# Wait until the CP knows the node's reasoners. This CP exposes the reasoner
# surface at /api/v1/discovery/capabilities (there is no /api/v1/nodes/{id}).
for _ in $(seq 1 30); do
  RC="$(curl -s --connect-timeout 3 -m 30 "$CP_URL/api/v1/discovery/capabilities?limit=500" \
        | grep -c "\"agent_id\":\"$NODE_ID\"" || true)"
  [[ "$RC" -ge 1 ]] && break
  sleep 1
done
[[ "${RC:-0}" -ge 1 ]] || { err "node never appeared in CP capabilities (see $RUN_DIR/node.log)"; exit 1; }
ok "pr-af-go node registered (pid $NODE_PID)"

# ---------------------------------------------------------------------------
# 5. Kick off the async review (repo_path + dry_run=true) and poll to terminal
# ---------------------------------------------------------------------------
read -r -d '' BODY <<JSON
{"input":{"repo_path":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$FIXTURE"),
"dry_run":true,
"depth":"quick"}}
JSON

log "POST /api/v1/execute/async/$NODE_ID.review"
RESP="$(curl -s --connect-timeout 3 -m 30 -X POST "$CP_URL/api/v1/execute/async/$NODE_ID.review" \
  -H 'Content-Type: application/json' -d "$BODY")"
EXEC_ID="$(printf '%s' "$RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("execution_id",""))' 2>/dev/null)"
WF_ID="$(printf '%s' "$RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("workflow_id",""))' 2>/dev/null)"
[[ -z "$EXEC_ID" ]] && { err "no execution_id (resp: $RESP)"; exit 1; }
log "execution_id=$EXEC_ID workflow_id=$WF_ID"

STATUS=""; REC=""
START=$(date +%s)
for _ in $(seq 1 120); do   # 120 * 3s = 6 min cap
  REC="$(curl -s --connect-timeout 3 -m 30 "$CP_URL/api/v1/executions/$EXEC_ID")"
  STATUS="$(printf '%s' "$REC" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))' 2>/dev/null)"
  case "$STATUS" in
    succeeded|success|completed|failed|error|cancelled|canceled|timeout) break;;
  esac
  sleep 3
done
WALL=$(( $(date +%s) - START ))
printf '%s' "$REC" > "$RUN_DIR/execution.json"
log "terminal status=$STATUS after ${WALL}s"

# ---------------------------------------------------------------------------
# 6. Assertions
# ---------------------------------------------------------------------------
echo; log "==================== ASSERTIONS ===================="

# (a) review succeeded
case "$STATUS" in succeeded|success|completed) rc=0;; *) rc=1;; esac
assert "$rc" "execution terminal status = succeeded (got: $STATUS)"

# (b) findings present  (c) review body non-empty
python3 - "$RUN_DIR/execution.json" <<'PY'
import json,sys
rec=json.load(open(sys.argv[1]))
res=rec.get("result") or {}
findings=res.get("findings") or []
body=((res.get("review") or {}).get("body")) or ""
open("/tmp/.praf_e2e_findings","w").write(str(len(findings)))
open("/tmp/.praf_e2e_bodylen","w").write(str(len(body)))
PY
NF="$(cat /tmp/.praf_e2e_findings 2>/dev/null || echo 0)"
BL="$(cat /tmp/.praf_e2e_bodylen 2>/dev/null || echo 0)"
[[ "${NF:-0}" -ge 1 ]]; assert $? "review produced findings (count: ${NF:-0})"
[[ "${BL:-0}" -ge 1 ]]; assert $? "review body is non-empty (chars: ${BL:-0})"

# (d) expected phases fired (from the mock invocation log)
LOG="$STATE/invocations.jsonl"
if [[ -f "$LOG" ]]; then
  # These are the roles the shim actually records once findings flow: anatomy,
  # at least one meta lens, review_dimension, and the layer pair evidence_verifier
  # + adversary (which only fire when review_dimension produced findings — the
  # regression that run 20260710-154635 caught). Roles like compound_finder /
  # verify_obligation / post_worthiness are scenario- or config-dependent, so
  # they are reported but not asserted.
  python3 - "$LOG" <<'PY' >/dev/null 2>&1
import json,sys
roles={json.loads(l)["role"] for l in open(sys.argv[1]) if l.strip()}
need_any_meta = roles & {"meta_semantic","meta_mechanical","meta_systemic"}
assert "anatomy" in roles, roles
assert need_any_meta, roles
assert "review_dimension" in roles, roles
assert "evidence_verifier" in roles, roles
assert "adversary" in roles, roles
PY
  assert $? "expected harness phases fired: anatomy, meta_*, review_dimension, evidence_verifier, adversary"
  ROLE_COUNTS="$(python3 -c 'import json,sys,collections
c=collections.Counter(json.loads(l)["role"] for l in open(sys.argv[1]) if l.strip())
print(dict(c))' "$LOG" 2>/dev/null)"
  log "shim role counts: ${ROLE_COUNTS:-<unavailable>}"
else
  err "no invocation log at $LOG"; ASSERT_FAILS=$((ASSERT_FAILS+1))
fi

# (e) zero GitHub writes — repo_path + dry_run means no PR post was attempted.
if grep -qi "api.github.com" "$RUN_DIR/node.log" 2>/dev/null; then
  err "node contacted api.github.com — expected zero GitHub writes"; ASSERT_FAILS=$((ASSERT_FAILS+1))
else
  ok "no GitHub API traffic (zero-GitHub-writes)"
fi

# ---------------------------------------------------------------------------
# 7. Summary
# ---------------------------------------------------------------------------
echo; log "==================== SUMMARY ===================="
log "wall clock:     ${WALL}s"
log "status:         $STATUS   findings=$NF  body_chars=$BL"
log "run dir:        $RUN_DIR  (node.log, cp.log, execution.json, scenario.json)"
log "invocation log: $LOG"
if [[ "$ASSERT_FAILS" -eq 0 ]]; then
  ok  "ALL ASSERTIONS PASSED"
  exit 0
else
  err "$ASSERT_FAILS assertion(s) FAILED"
  exit 1
fi
