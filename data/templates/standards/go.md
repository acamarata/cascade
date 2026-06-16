---
id = "standard-go"
version = "1.0.0"
tier = "any"
stacks = ["go"]
project_shapes = []
description = "Go coding standard: gofmt, error wrapping, context propagation, table-driven tests."
---

# Go Coding Standard

## Formatting and Linting

- All code formatted with `gofmt` (or `goimports`). No unformatted files on main.
- Lint with `golangci-lint` using at minimum: `errcheck`, `govet`, `staticcheck`,
  `gosimple`, `ineffassign`, `unused`. Config in `.golangci.yml`.
- `golangci-lint run ./...` must exit 0 in CI.

## Error Handling

- Never ignore errors. Assign every multi-return with `, err` and check it.
- Wrap errors at package boundaries: `fmt.Errorf("operation failed: %w", err)`.
  Use `%w` (not `%v`) so callers can `errors.Is` / `errors.As`.
- Define sentinel errors (`var ErrNotFound = errors.New("not found")`) for
  conditions callers need to distinguish.
- No `panic` in library code except for programmer errors (nil pointer from
  caller violating a documented precondition).

## Context Propagation

- Every function that performs I/O, makes an RPC, or can block takes
  `ctx context.Context` as its first parameter.
- Never store a context in a struct field. Pass it explicitly through the call chain.
- Respect cancellation: check `ctx.Err()` in loops or before expensive ops.

## Code Structure

- Package layout: `cmd/` for binaries, `internal/` for non-exported packages,
  top-level packages for the public API.
- Files: ≤300 lines. One file = one primary type or cohesive set of functions.
- Exported identifiers have a Go doc comment (`// FuncName does …`).
- Avoid stuttering: if the package is `user`, the type is `User`, not `UserUser`.

## Tests

- Use table-driven tests with `t.Run` subtests for parameterized cases.
- Test files live in the same package (`package foo_test` for black-box,
  `package foo` for white-box).
- Run `go test ./... -race` in CI. The race detector is not optional.
- Coverage target: ≥75% statements (`go test -coverprofile`).

## Dependencies

- Add dependencies with `go get`; commit both `go.mod` and `go.sum`.
- Keep the module graph shallow. Prefer the standard library over third-party
  packages for common operations.
