# PR-AF Go node — multi-stage build.
#
# Build from the PR-AF repo ROOT so the go/ module is in the build context and
# the paths below (go/go.mod, go/) resolve. The docker-compose.go.yml add-on
# builds it exactly this way (build.context: ., dockerfile: go/Dockerfile):
#
#     docker build -f go/Dockerfile -t pr-af:latest .
#
# Unlike the SWE-AF Go port, the AgentField Go SDK is a REAL versioned require
# resolved from proxy.golang.org — so there is NO sparse SDK clone stage, no
# GOWORK=off, and no `replace` dance. `go mod download` pulls the SDK and every
# other dependency straight from the module proxy, cache-keyed on go.mod/go.sum.

# ---------------------------------------------------------------------------
# Stage 1 — builder: fetch modules from the proxy, build the static binary.
# golang 1.23 satisfies go.mod's `go 1.21` directive.
# ---------------------------------------------------------------------------
FROM golang:1.23-bookworm AS builder

WORKDIR /src

# Prime the module cache from go.mod/go.sum first so dependency downloads cache
# independently of source edits (this layer re-runs only when go.mod/go.sum change).
COPY go/go.mod go/go.sum ./
RUN go mod download

# Copy the rest of the module and build a static binary.
COPY go/ ./
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/pr-af ./cmd/pr-af

# ---------------------------------------------------------------------------
# Stage 2 — runtime: slim Debian mirroring the Python image (opencode CLI + a
# non-root praf user), but shipping the single static Go binary instead of a
# Python runtime. The entrypoint is byte-identical to the Python one — it
# generates opencode.json from PR_AF_MODEL at container start.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

ARG OPENCODE_VERSION=1.17.15

# PR_AF_HARNESS_BIN remains unset by default; set it only to override every
# provider's executable path.
ENV AGENTFIELD_SERVER=http://agentfield:8080 \
    PR_AF_PROVIDER=opencode \
    PR_AF_MODEL=openrouter/moonshotai/kimi-k2.5 \
    PORT=8007 \
    NODE_ID=pr-af \
    HOME=/home/praf \
    PATH=/home/praf/.opencode/bin:${PATH} \
    XDG_DATA_HOME=/home/praf/.local/share \
    PR_AF_WORKDIR=/workspaces

# System deps: ca-certificates (HTTPS to the LLM provider + GitHub), curl
# (healthcheck + opencode installer), git (repo_path reviews clone/fetch/checkout).
# Create the non-root praf user (uid/gid 10001) and install the opencode CLI as
# that user so it lands under /home/praf/.opencode (on PATH above).
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git && \
    groupadd --gid 10001 praf && \
    useradd --uid 10001 --gid praf --create-home --home-dir /home/praf --shell /bin/sh praf && \
    su -s /bin/sh praf -c "curl -fsSL https://opencode.ai/install | bash -s -- --version ${OPENCODE_VERSION} --no-modify-path" && \
    mkdir -p /workspaces /home/praf/.local/share && \
    chown -R praf:praf /workspaces /home/praf && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/pr-af /usr/local/bin/pr-af
COPY go/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

USER praf

EXPOSE 8007

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD curl -f http://localhost:8007/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/pr-af"]
