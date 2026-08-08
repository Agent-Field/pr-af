# PR-AF Go port — build/test targets. Run from the go/ directory.
# Mirrors the acceptance gate used by every port task and CI (design §F, §G):
#   go build ./... && go vet ./... && go test ./...
#
# The AgentField Go SDK is a REAL versioned require resolved from
# proxy.golang.org (no replace directive), so no GOWORK/replace dance is needed
# for CI/Docker. A gitignored go.work is the local dev path; set GOWORK=off to
# ignore it explicitly.

.PHONY: build vet test check fmt lint run docker-build docker-up docker-down

# Compile every package in the module.
build:
	go build ./...

# Static analysis (go vet) across the module.
vet:
	go vet ./...

# Unit tests across the module.
test:
	go test ./...

# The full local gate CI runs: build + vet + tests.
check: build vet test

# Format every Go source file in place.
fmt:
	gofmt -w .

# Optional lint pass; no-op with a hint when golangci-lint is not installed.
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || \
		echo "golangci-lint not installed; skipping (install: https://golangci-lint.run)"

# Run the PR-AF node (pr-af, default port 8007).
run:
	go run ./cmd/pr-af

# --- Docker (multi-stage image + Go-node compose add-on) ------------------
# The Dockerfile build context is this go/ directory (design §E.1): the SDK is
# a real require pulled by `go mod download`, so there is no SDK clone stage and
# no build-arg. The compose add-on (docker-compose.go.yml) lives at the repo
# root and joins the Python pr-af stack's external network + workspaces volume.
# Override the image tag with: make docker-build IMAGE=myrepo/pr-af:dev
IMAGE ?= pr-af:latest

# Build the multi-stage Go image. The Dockerfile expects the REPO ROOT as the
# build context (it COPYs go/go.mod, go/ ...), so the context is the parent dir.
docker-build:
	docker build -f Dockerfile -t $(IMAGE) ..

# Bring up the Go node (pr-af-go:8007) as an ADD-ON to the Python stack. Start
# the Python stack first (`docker compose up` — it owns the control plane +
# shared network); this add-on joins that network as an external reference.
docker-up:
	docker compose -f ../docker-compose.go.yml up --build

# Tear the Go-node add-on down.
docker-down:
	docker compose -f ../docker-compose.go.yml down
