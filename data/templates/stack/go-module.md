---
id = "go-module"
version = "1.0.0"
tier = "any"
stacks = ["go"]
project_shapes = []
description = "Go module layout with golangci-lint and testify — Go 1.22+"
---

# CASCADE Instructions — Go Module

> Stack: Go 1.22+ · Module layout · testify · golangci-lint
> Tier: any (drop at PRC or PAC)

Use `{{project_name}}` for the module name, `{{go_version}}` for the minimum Go version (e.g. `1.22`), and `{{repo_url}}` for the module path (e.g. `github.com/acme/mylib`).

---

## Module / Package Layout

Follow Go standard layout. Keep packages small and purposeful.

```
{{project_name}}/
├── cmd/
│   └── {{project_name}}/       # main package (binary entry point)
│       └── main.go
├── internal/                    # unexported packages; not importable externally
│   └── core/
│       └── core.go
├── pkg/                         # exported packages safe for external use
├── testdata/                    # test fixtures (excluded from builds)
├── go.mod
├── go.sum
├── .golangci.yml
└── Makefile
```

One package per directory. Never put multiple packages in one directory. Name the package after the directory (`package core` in `internal/core/`).

---

## Build & Tooling

**Minimum version.** `go.mod` must declare:

```
go {{go_version}}
```

**Common commands:**

```bash
# Build
go build ./...

# Run entry point
go run ./cmd/{{project_name}}

# Tidy dependencies
go mod tidy
```

**Makefile targets (standard):**

```makefile
.PHONY: build test lint tidy

build:
	go build ./...

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
```

---

## Testing Convention

Use `testify` for assertions. Keep test files next to the code they test (`_test.go` suffix).

```go
import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestFoo(t *testing.T) {
    result, err := Foo("input")
    require.NoError(t, err)        // fail fast on errors
    assert.Equal(t, "expected", result)
}
```

- `require.*` for errors that would make the rest of the test meaningless.
- `assert.*` for normal value comparisons.
- Table-driven tests for multiple cases (`[]struct{ name, input, want string }`).
- Integration tests go in a `_integration_test.go` file with `//go:build integration`.

---

## Lint & Format

**golangci-lint** is the single lint runner. Minimum `.golangci.yml`:

```yaml
linters:
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused
    - gofmt
    - goimports
run:
  timeout: 5m
```

Run format before commit:

```bash
gofmt -w .
goimports -w .    # if goimports is in the linter set
```

Do not commit code with `gofmt` diffs. CI runs `golangci-lint run ./...` and fails on any warning.

---

## Common Pitfalls

- **`go mod tidy` must stay clean.** Any `go.sum` drift fails CI. Run `go mod tidy` after adding/removing deps.
- **Avoid `init()` functions.** They make dependency order invisible and slow test startup.
- **Error wrapping.** Use `fmt.Errorf("context: %w", err)` so callers can `errors.Is`/`errors.As` through the chain.
- **Race detection.** Always run `go test -race`; data races are silent without it.
- **`internal/` vs `pkg/`.** If in doubt, put it in `internal/`. Promoting to `pkg/` is easy; demoting breaks dependants.
