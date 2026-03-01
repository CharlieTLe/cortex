# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Cortex is a horizontally scalable, highly available, multi-tenant, long-term storage solution for Prometheus metrics. This branch (`operator`) adds a Kubernetes operator that manages Cortex clusters via a single `Cortex` Custom Resource.

The repository contains two Go modules:
- **Root module** (`github.com/cortexproject/cortex`) — the core Cortex server and libraries
- **Operator module** (`operator/`) — a Kubebuilder-based Kubernetes operator with its own `go.mod`

## Build Commands

### Core Cortex

```bash
make                           # Build all (runs in Docker container by default)
make BUILD_IN_CONTAINER=false  # Build locally without Docker
make exes                      # Build binaries only
make protos                    # Generate protobuf files
make lint                      # Run all linters (golangci-lint, misspell, etc.)
make doc                       # Generate config documentation (run after changing flags/config)
make ./cmd/cortex/.uptodate    # Build Cortex Docker image for integration tests
```

### Operator

All operator commands run from `operator/`:

```bash
cd operator/
make manifests       # Regenerate CRD, RBAC, and webhook manifests (run after changing API types)
make generate        # Regenerate DeepCopy methods (run after changing API types)
make build           # Build operator binary
make docker-build    # Build Docker image (IMG=cortex-operator:dev)
make test            # Run unit + envtest tests
make test-e2e        # Run e2e tests (requires Kind cluster)
make lint            # Run golangci-lint
make kind-setup      # Full local dev: Kind + CubeFS + CRDs + operator + sample CR
make kind-redeploy   # Rebuild and restart operator in existing Kind cluster
make kind-status     # Show pod/service status
make kind-teardown   # Delete Kind cluster
```

Run operator locally without cluster deployment:
```bash
cd operator/
bin/kustomize build config/crd | kubectl apply --server-side -f -
ENABLE_WEBHOOKS=false go run ./cmd/main.go
```

## Testing

### Core Cortex Unit Tests

```bash
go test -timeout 2400s -tags "netgo slicelabels" ./...
```

### Core Cortex Integration Tests

Require Docker and the Cortex image built first:

```bash
make ./cmd/cortex/.uptodate    # Build image first

# Run all integration tests
go test -v -tags=integration,requires_docker,integration_alertmanager,integration_memberlist,integration_querier,integration_ruler,integration_query_fuzz ./integration/...

# Run a specific integration test
go test -v -tags=integration,integration_ruler -timeout 2400s -count=1 ./integration/... -run "^TestRulerAPISharding$"
```

### Operator Tests

```bash
cd operator/
make test            # Unit + envtest (config builder, controller, webhooks)
make test-e2e        # E2e on Kind cluster (uses Ginkgo)
```

## Vendored Dependencies

Go modules are vendored in `vendor/` (root module only). When upgrading a dependency:

```bash
go get github.com/some/dependency@version
go mod vendor
go mod tidy
```

Do not modify vendored code directly. Check `vendor/` for upstream library code when investigating behavior.

## Code Formatting

```bash
goimports -local github.com/cortexproject/cortex -w ./path/to/file.go
```

Import order: stdlib, third-party packages, internal Cortex packages (separated by blank lines).

## Architecture

### Core Cortex Components

**Write path:** Distributor (stateless) → Ingester (semi-stateful, TSDB blocks)
**Read path:** Query Frontend (optional) → Querier (stateless) → Ingesters + Store Gateway
**Storage:** Compactor (stateless), Store Gateway (semi-stateful) — blocks in S3/GCS/Azure/Swift
**Optional:** Ruler, Alertmanager, Configs API

**Key patterns:** Hash ring (Consul/Etcd/memberlist), multi-tenancy via `X-Scope-OrgID` header, TSDB blocks storage

**Entry points:** `cmd/cortex/main.go`, `pkg/cortex/cortex.go` (service orchestration)

### Operator Architecture

The operator lives entirely in `operator/` with this structure:

- `api/v1alpha1/` — CRD type definitions (`Cortex` custom resource spec/status)
- `internal/controller/` — Reconciliation loop, creates K8s workloads from CR spec
- `internal/config/` — Config builder: converts CR spec → Cortex YAML configuration
- `internal/webhook/` — Validation and defaulting webhooks
- `cmd/main.go` — Operator entry point
- `config/` — Kustomize manifests (CRDs, RBAC, samples)
- `hack/dev/` — Local development helpers (CubeFS, sample CRs, operator deployment)
- `test/e2e/` — End-to-end tests

**Reconciliation flow:**
1. Generate Cortex config YAML from CR spec (or use `externalConfig` escape hatch)
2. Create/update ConfigMap with config (SHA-256 hash in pod annotations triggers rolling restarts)
3. Reconcile each component: Services, Deployments (distributor, querier, query-frontend), StatefulSets with PVCs (ingester, store-gateway, compactor), PodDisruptionBudgets
4. Update CR status conditions (ConfigReady, Ready, Degraded)

All created resources have OwnerReferences for automatic garbage collection on CR deletion.

**When modifying API types** (`api/v1alpha1/`), always run `make manifests generate` afterward.

## Code Conventions

- **No global variables** — use dependency injection
- **Metrics:** Register with `promauto.With(reg)`, never use global prometheus registerer
- **Config naming:** YAML uses `snake_case`, CLI flags use `kebab-case`
- **Logging:** Use `github.com/go-kit/log` (not `github.com/go-kit/kit/log`)

## PR Requirements

- Sign commits with DCO: `git commit -s -m "message"`
- Run `make doc` if config/flags changed in core Cortex
- Include CHANGELOG entry for user-facing changes
