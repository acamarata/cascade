# Stack Template: Go Monolith

**Tier:** APC · **Stack:** Go monolith (single module, single binary) · **Language:** Go 1.21+

## Idiomatic Layout

```
cmd/
  {appname}/
    main.go             # Entry point; wires dependencies
internal/               # Private packages; not importable externally
  {domain}/
    handler.go          # HTTP handlers (net/http or chi/fiber)
    service.go          # Business logic
    repository.go       # Data access interface + implementation
    model.go            # Domain types
  middleware/           # HTTP middleware
  config/               # Config struct; loaded from env
  database/             # DB connection, migrations runner
  testutil/             # Test helpers (internal use only)
pkg/                    # Public reusable packages (if any)
migrations/             # SQL migration files (versioned)
scripts/                # Build/deploy helper scripts
go.mod
go.sum
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Dependency injection via constructor functions, not global state
- Interfaces defined at the consumer (internal/domain), not in the implementation
- One interface per dependency; test doubles are simple structs implementing the interface
- HTTP handlers are thin: bind request → call service → write response
- Database: `database/sql` or `pgx` directly; no ORM; queries in `repository.go`

## Key Commands

```bash
go build ./...          # Build all
go test ./...           # All tests
go test -race ./...     # Race detector
go vet ./...            # Static analysis
staticcheck ./...       # Extended static analysis
golangci-lint run       # Full lint suite
go mod tidy             # Sync go.mod/go.sum
```

## Engineering Rules

- `go.mod` module path matches repository URL
- All exported functions have doc comments
- No global mutable state; everything injected via constructors
- File ceiling: ≤400 lines per .go file; split by concern beyond limit
- Migrations in `migrations/` with monotonic numeric prefix: `001_create_users.sql`

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/proxy-first-data-access.md`
- `.cascade/rules/master-lists-protocol.md`
