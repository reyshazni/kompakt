# Development Setup

## Prerequisites

- Go 1.23+
- Docker
- kind (for local clusters)
- kubectl
- Helm 3
- golangci-lint

## Clone and build

```bash
git clone https://github.com/reyshazni/kompakt.git
cd kompakt
make deps
make build
```

## Run tests

```bash
# Unit tests
make test

# Specific package
go test ./internal/ledger/...

# E2e tests (requires kind cluster)
make kind-create
make test-e2e
```

## Run locally

```bash
# Create a kind cluster
make kind-create

# Install CRDs
make install

# Run the controller locally (connects to kind)
make run
```

## Build and deploy to kind

```bash
make docker-build IMG=kompakt:dev
make kind-load IMG=kompakt:dev
make deploy IMG=kompakt:dev
```

## TDD workflow

All development follows RED, GREEN, REFACTOR:

1. **RED**: Write a failing test first. Run it, confirm it fails for the right reason.
2. **GREEN**: Write the minimum code to make the test pass. Nothing more.
3. **REFACTOR**: Clean up the implementation while keeping tests green.

Rules:

- Never write implementation code without a failing test
- Run `go test ./path/to/package/...` after each step
- Do not run the full test suite on every edit. Only run the package you are working on.
- Full `make test` runs at verification time before commits

## Pre-commit verification

Before every commit:

```bash
make fmt vet lint test
```

All four must pass.

## Make targets

| Target | Description |
|---|---|
| `make build` | Build the manager binary |
| `make run` | Run locally against the configured cluster |
| `make test` | Run unit tests with coverage |
| `make test-e2e` | Run e2e tests (requires kind) |
| `make lint` | Run golangci-lint |
| `make fmt` | Run go fmt |
| `make vet` | Run go vet |
| `make generate` | Generate deepcopy and CRD manifests |
| `make manifests` | Generate RBAC and CRD manifests |
| `make install` | Install CRDs into the cluster |
| `make deploy` | Deploy controller to the cluster |
| `make docker-build` | Build docker image |
| `make kind-create` | Create a kind cluster |
| `make kind-delete` | Delete the kind cluster |
| `make kind-load` | Load image into kind |
| `make deps` | Download and tidy Go modules |

## Code conventions

- Follow upstream Kubernetes Go conventions
- Use structured logging via `logr`
- All reconciler logic must be idempotent
- Webhook p99 latency under 50ms
- CRD field names: camelCase in JSON, PascalCase in Go
- Error messages: lowercase, no trailing punctuation
- Test files colocated: `foo.go` -> `foo_test.go`
- No em dashes (U+2014, U+2013) anywhere
- No ASCII art or box-drawing characters

## Docs

```bash
pip install -r docs/requirements.txt
mkdocs serve
```

Docs are served at `http://localhost:8000`.
