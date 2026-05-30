# Stack Template: Go Workspace (Multi-Module)

**Tier:** APC · **Stack:** Go workspace (`go.work`) with multiple modules · **Language:** Go 1.21+

## Idiomatic Layout

```
go.work                 # Workspace file listing member modules
go.work.sum
services/
  api/                  # HTTP API service module
    cmd/api/main.go
    internal/
    go.mod
  worker/               # Background worker module
    cmd/worker/main.go
    internal/
    go.mod
packages/               # Shared library modules
  shared/               # Cross-service types, utilities
    go.mod
  events/               # Event definitions
    go.mod
migrations/             # Shared DB migrations (or per-service)
scripts/
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Each module (`go.mod`) is independently buildable and testable
- Shared code in `packages/`; never import `internal/` across module boundaries
- Service-to-service communication via message queue or gRPC (not direct import)
- `go.work` is development-only; CI builds each module independently to verify no accidental coupling
- Shared interfaces in `packages/shared/`; implementations in each service

## Key Commands

```bash
go work sync            # Sync workspace
go build ./...          # Build all workspace modules
go test ./...           # Test all modules
go vet ./...            # Vet all
golangci-lint run ./... # Lint all
# Per-module:
cd services/api && go test ./...
```

## Engineering Rules

- `go.work` committed only in development; CI uses module-by-module build without go.work
- Each module has its own `go.mod` with minimum required dependencies
- No circular imports across modules; dependency graph is a DAG
- File ceiling: ≤400 lines per .go file; packages split by domain concern
- `.cascade/docs/MASTER-MODULES.md` tracks every module and its dependencies

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/code-duplication-thresholds.md`
- `.cascade/rules/master-lists-protocol.md`
