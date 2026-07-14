# PR-AF — Go node

A Go port of the PR-AF agentic code-review node. It registers the same reasoner
surface under the same names as the Python node, exposes a byte-compatible
HTTP API, and reports every pipeline phase as its own tracked execution, so the
control-plane DAG UI renders the same multi-node orchestration graph as the
Python node (see [Pipeline DAG on the control plane](#pipeline-dag-on-the-control-plane)).
The Python package under `src/pr_af/` is untouched; this port lives entirely
under `go/`.

One binary:

| Binary   | Node ID    | Default port | Role                                   |
|----------|------------|--------------|----------------------------------------|
| `pr-af`  | `pr-af-go` | `8007`       | Full review pipeline (intake → review) |

Module path: `github.com/Agent-Field/pr-af/go`.

## Opt-in alongside Python

The Python node is the **default**: `pr-af` on `:8004`, unchanged. This Go port
registers **separately** under a distinct identity — `pr-af-go` on `:8007` — so
both nodes can run against **one** control plane at the same time. Nothing is
replaced; callers **opt in** by targeting the `-go` reasoner path, e.g.

```bash
curl -X POST http://localhost:8080/api/v1/execute/async/pr-af-go.review \
  -H 'Content-Type: application/json' \
  -d '{"input":{"pr_url":"https://github.com/owner/repo/pull/123"}}'
```

`NODE_ID` / `PORT` still override the defaults if you want a different id/port.

## Pipeline DAG on the control plane

The Python node's review is not one execution — every phase
(`intake_phase`, `anatomy_phase`, the three `meta_*` selectors, each parallel
`review_dimension`, `adversary_phase`, `evidence_verifier`, the obligation and
compound reasoners, `coverage_gate`) runs as a **tracked child execution** of
the parent `review`, and the control-plane UI renders the run as that DAG.
Python gets this from its `@router.reasoner()` wrapper, which routes direct
in-process calls through workflow instrumentation.

The Go port reproduces the same graph through the SDK's `Agent.CallLocal`:
the orchestrator's phase seams (`orch.callLocalSeams`) invoke each phase under
its registered reasoner name instead of calling the function directly. Each
call builds a child execution context from the incoming request's context and
emits `running` / `succeeded` / `failed` workflow events to the control plane,
which mirrors them into the executions table — one DAG node per phase, parented
under the `review` execution, same shape as Python.

Mechanics worth knowing:

- **Same code path either way.** A CallLocal-routed phase goes through the
  registered handler (`afx.Bind` into the typed input → the same `reasoners.*`
  function with the same deps). `afx.ToMap` keeps nested values typed so
  custom marshalers (notably `OrderedPatches`' insertion-ordered
  `diff_patches`) survive the round trip byte-for-byte.
- **Best-effort reporting.** Workflow events are fire-and-forget: an
  unreachable control plane logs a warning and the review proceeds — the DAG
  is observability, never a failure mode.
- **Stubs opt out.** `orch.Deps.Local` is nil in unit tests and stub
  harnesses, which keeps the plain direct-call seams; production wiring
  (`node.BuildAgent`) always points it at the live agent.

## Depending on the AgentField Go SDK

Unlike the SWE-AF Go port, this module depends on the AgentField Go SDK
(`github.com/Agent-Field/agentfield/sdk/go`) via a **real, committed `require`**
resolved from `proxy.golang.org` — there is **no `replace` directive** and no
sibling checkout to lay out. `go build ./...` works out of the box against the
pinned SDK pseudo-version in `go.mod`.

- **CI / Docker.** `go mod download` pulls the SDK (and every other dependency)
  straight from the module proxy. No `GOWORK=off`, no sparse clone.
- **Dev — optional Go workspace.** A gitignored `go.work` (spanning this module
  and a local `agentfield/sdk/go` checkout) is the way to develop against
  unreleased SDK changes; with it present, `go build ./...` picks up local SDK
  edits live. It is never committed.

Bumping the SDK is a deliberate, reviewable change: bump the `require`
pseudo-version in `go.mod`, and move the Docker builder image tag / CI
`setup-go` version together if the SDK's own `go` directive ever advances past
`go 1.21`.

## Build & run locally

From `go/`:

```bash
make build          # go build ./...
make vet            # go vet ./...
make test           # go test ./...
make check          # vet + test
make fmt            # gofmt -w .
make run            # run the node (pr-af-go, :8007)
```

`make run` needs a control plane reachable at `AGENTFIELD_SERVER` (default
`http://localhost:8080`). The node reads all configuration from the environment
at startup.

## Docker

The image is a multi-stage build and is **simpler than SWE-AF's** — because the
SDK is a real proxy-resolved `require`, the builder just runs `go mod download`
(no SDK clone stage). The runtime stage is a slim Debian mirroring the Python
image: the pinned `opencode` CLI (`1.17.15`), a non-root `praf` user, and the
**same** `docker-entrypoint.sh` (a byte copy of the Python one) that generates
`opencode.json` from `PR_AF_MODEL` at container start.

The build context is the **repo root** so the `go/` module is in context:

```bash
# from the repo root
docker build -f go/Dockerfile -t pr-af-go:latest .
```

### Compose: opt-in add-on to the Python stack

`docker-compose.go.yml` (at the repo root) is an **add-on**, not a standalone
stack. It defines only the Go node and joins the Python stack's compose network
as an external reference, sharing the control plane (`agentfield`) and the
`workspaces` volume the Python stack brings up. The Python `docker-compose.yml`
is left untouched. Start the Python stack first, then layer the Go node:

```bash
docker compose up -d                          # Python stack (control plane + pr-af :8004)
docker compose -f docker-compose.go.yml up -d # adds pr-af-go :8007
```

Adds:

| Service    | Port   | Node id    | Notes                    |
|------------|--------|------------|--------------------------|
| `pr-af-go` | `8007` | `pr-af-go` | full review pipeline     |

The control plane (`:8080`) and the `workspaces` volume come from the Python
stack — the Go add-on joins them via the external `pr-af_default` network and
`pr-af_pr-af-workspaces` volume. This assumes the Python stack was brought up
with the default project name `pr-af` (the Python compose has no explicit
`name:`, so its project name is the checkout directory's basename); see the
compose file header for the `COMPOSE_PROJECT_NAME` override. Health:
`curl -f http://localhost:8007/health`.

## Environment variables

The node is configured entirely through the environment.

| Variable                    | Purpose                                                         |
|-----------------------------|----------------------------------------------------------------|
| `OPENROUTER_API_KEY`        | LLM provider key (OpenRouter) — required                       |
| `GH_TOKEN`                  | GitHub token (`repo` scope) for reading PRs and posting reviews |
| `AGENTFIELD_SERVER`         | Control-plane URL (default `http://localhost:8080`)            |
| `AGENTFIELD_API_KEY`        | Control-plane API key (if the CP has auth enabled)             |
| `NODE_ID`                   | Node ID (default `pr-af-go`)                                    |
| `PORT`                      | Listen port (default `8007`)                                   |
| `PR_AF_PROVIDER`            | Harness provider (default `opencode`)                          |
| `PR_AF_MODEL`               | Harness model (default `openrouter/moonshotai/kimi-k2.5`)      |
| `PR_AF_MAX_COST_USD`        | Per-run cost ceiling in USD (default `2.0`)                    |
| `PR_AF_MAX_DURATION_SECONDS`| Per-run wall-clock ceiling in seconds (default `3600`)         |
| `AGENTFIELD_HARNESS_IDLE_SECONDS` | Harness no-output watchdog window in seconds (default `360`) — harness CLIs in JSON mode emit events only at completion boundaries, so long single completions look silent |
| `HAX_API_KEY`               | Optional — enables the HITL review-approval gate when set      |

Note: the code default model is `minimax/minimax-m2.5`, while the Docker image /
compose / manifest set `PR_AF_MODEL=openrouter/moonshotai/kimi-k2.5`. The env
var always wins; both defaults are intentional (they mirror the Python node).

## Deployment: `af install --path go`

Because the SDK is a committed real `require` (no out-of-tree `replace` for the
installer to reject), the Go node **can** be installed via the AgentField
package installer, pointing it at the `go/` subdirectory:

```bash
af install https://github.com/Agent-Field/pr-af --path go
```

This reads `go/agentfield-package.yaml` (node id `pr-af-go`, default port
`8007`) and builds `./cmd/pr-af`. The `--path` subdirectory selector requires
**agentfield ≥ v0.1.108** (the installer's `--path` support, merged in
agentfield#750); on an older control plane, prefer the Docker image / compose /
binary path above.
