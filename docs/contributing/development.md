# Development Setup

## Contributing workflow

1. **Fork** the repo on GitHub
2. **Clone** your fork locally
3. **Create a branch** for your change
4. **Make changes**, following the TDD workflow below
5. **Run `make verify`** to validate everything
6. **Push** to your fork
7. **Open a PR** against `main`

CI runs automatically on every PR. All checks must pass before merge.

Direct pushes to `main` are restricted to maintainers.

## Prerequisites

- Go 1.23+
- Docker
- kind (for local clusters)
- kubectl
- Helm 3
- golangci-lint
- kustomize

## Clone and build

```bash
# Fork first, then:
git clone https://github.com/<your-username>/kompakt.git
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
- Full `make verify` runs before pushing

## Pre-commit verification

Before every commit, run the full validation suite:

```bash
make verify
```

This runs fmt, vet, lint, unit tests, helm lint, and kustomize build. All must pass.

## CI checks

Every PR is validated by these checks (all must pass):

| Check | What it does |
|---|---|
| Lint | go fmt, go vet, golangci-lint |
| Unit Tests | `go test -race ./...` |
| Helm Lint | Lint and template the Helm chart |
| Kustomize Build | Validate kustomize overlay builds |
| Build Image | Docker build (no push) |
| E2E (K8s 1.30) | Full e2e on kind cluster |
| E2E (K8s 1.31) | Full e2e on kind cluster |

## Make targets

| Target | Description |
|---|---|
| `make verify` | Run all checks (CI equivalent) |
| `make build` | Build the manager binary |
| `make run` | Run locally against the configured cluster |
| `make test` | Run unit tests with coverage |
| `make test-e2e` | Run e2e tests (requires kind) |
| `make lint` | Run golangci-lint |
| `make fmt` | Run go fmt |
| `make vet` | Run go vet |
| `make helm-lint` | Lint and template Helm chart |
| `make kustomize-build` | Validate kustomize build |
| `make generate` | Generate deepcopy and CRD manifests |
| `make manifests` | Generate RBAC and CRD manifests |
| `make install` | Install CRDs into the cluster |
| `make deploy` | Deploy controller to the cluster |
| `make docker-build` | Build docker image |
| `make kind-create` | Create a kind cluster |
| `make kind-delete` | Delete the kind cluster |
| `make kind-load` | Load image into kind |
| `make deps` | Download and tidy Go modules |
| `make open-docs` | Serve docs locally at localhost:4400 |

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
make open-docs
```

Docs are served at `http://localhost:4400`. Changes to `docs/` or `mkdocs.yml` auto-deploy to GitHub Pages on merge to `main`.
