# CLI output contract

The output contract every `cascade` command is built against (`D/S-06.T5`,
`R-14.7`). Package home: [`internal/output`](../internal/output) — TTY/color
detection, the stdout=data / stderr=diagnostics stream split, the `--json`
versioned envelope, the NDJSON stream format, and the seam
(`internal/output.Writer`) that keeps command code from ever touching
`os.Stdout`/`os.Stderr` directly.

This document is the wire-level companion to
[`docs/reference/error-taxonomy.md`](reference/error-taxonomy.md), which
owns the frozen 14-kind taxonomy and its exit-code / JSON-RPC-code tables
(`R-14.3`) — this document never restates that table, only how `internal/output`
consumes it.

## TTY / non-TTY behavior matrix

| Signal | Effect |
|---|---|
| stdout is a terminal (`os.ModeCharDevice`, stdlib-only — no cgo, no isatty syscall) | Interactive defaults: color allowed (subject to the precedence below), `Progress` emits. |
| stdout is **not** a terminal (piped, redirected, or under CI) | `Progress` is suppressed unconditionally — only data and final status emit. Color is also disabled (see precedence below). |
| `-q` / `--quiet` | `Progress` is suppressed, regardless of TTY-ness. Does **not** suppress `Warn`, `Debug`, or an error (`Fail`). |
| `-v` / `--verbose` | `Debug` emits. Mutually exclusive with `--quiet` at the cobra layer (`cmd/cascade/root.go`'s `PersistentPreRunE`) — invalid-input if both are set. |

`Warn` is never suppressed by `--quiet` or non-TTY: a warning that silently
vanished under either would defeat its purpose. `Fail` (the error path) is
likewise never suppressed — an error must always be visible, on the stream
appropriate to the active mode (see below).

## stdout = data / stderr = diagnostics

| Stream | Carries |
|---|---|
| stdout | Records, the `--json` envelope (success **and** failure — see below), NDJSON lines, human-mode `Result`/`Println` output. |
| stderr | `Progress`, `Warn`, `Debug`, and human-mode `Fail` diagnostics. |

Command code never writes to `os.Stdout`/`os.Stderr` (or the
implicitly-stdout `fmt.Print`/`fmt.Println`/`fmt.Printf`) directly under
`cmd/`. The `.golangci.yml` `forbidigo` rule this ticket adds fails the
build on any such write in `cmd/**`, with `internal/output` itself the
only code in the module allowed to name the real streams
(`output.NewDefault`). Verified with a seeded violation: a direct
`fmt.Fprintln(os.Stderr, ...)` added to `cmd/cascade/main.go` fails lint
red; removing it returns the tree to green.

## `--json` versioned envelope

Every command's `--json` output is exactly one `Envelope` object
(`internal/output/envelope.go`), on stdout, indented for readability
(a single invocation emits one envelope, not a stream — see NDJSON below
for the streaming case). `Envelope.Version` (currently `1`,
`output.EnvelopeVersion`) travels in every response so a script can detect
a future incompatible shape change before parsing the rest; the version
bumps only on a removed/renamed/retyped field, never for an additive one.

```go
type Envelope struct {
    Version int            `json:"version"`
    OK      bool           `json:"ok"`
    Data    any            `json:"data,omitempty"`
    Error   *EnvelopeError `json:"error,omitempty"`
}

type EnvelopeError struct {
    Kind    string `json:"kind"`    // taxonomy Kind's wire name, e.g. "not-found"
    Code    int    `json:"code"`    // pkg/cascade's JSON-RPC code for Kind — same table plugin RPC uses
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}
```

`OK` and `Error` are mutually exclusive: a success envelope has `ok: true`
and `data` (when there is a result); a failure envelope has `ok: false` and
`error`. **The error envelope is emitted on stdout, not stderr** — the
envelope IS the command's data output, success or failure alike, so a
script parsing `--json` output always finds the result (or the reason it
failed) on the one stream it is already reading. Human-mode failures go to
stderr instead (`Fail`'s human branch); only `--json` mode changes which
stream carries the error.

`EnvelopeError.Kind`/`Code` are built from `pkg/cascade.KindOf` and
`pkg/cascade.NewRPCError` — the same taxonomy and the same JSON-RPC code
table `D/S-06.T3`'s wire framework and plugin RPC use (see
[error-taxonomy.md § Wire mappings](reference/error-taxonomy.md#wire-mappings)).
A non-taxonomy error (one that never passed through `pkg/cascade`) falls
back to `kind: "internal"` / `code: -32000`, mirroring
`cascade.NewRPCError`'s own fallback so the two never drift apart.

### Golden fixtures

`internal/output/testdata/golden_envelope_ok.json` and
`golden_envelope_err.json` pin the exact byte shape (`internal/output/envelope_test.go`).
Both are deterministic on purpose: no timestamp, no absolute path, no
hostname, and a typed struct (not a multi-key map) for the success payload
— `encoding/json` sorts map keys alphabetically regardless, but the typed
struct also keeps field order self-evidently stable to a human reader.

```json
{
  "version": 1,
  "ok": true,
  "data": {
    "name": "widget",
    "count": 3
  }
}
```

```json
{
  "version": 1,
  "ok": false,
  "error": {
    "kind": "not-found",
    "code": -32001,
    "message": "not-found: widget \"foo\" not found"
  }
}
```

## NDJSON stream format

Streaming commands (`--stream`, `fleet sessions --watch`, etc.) emit one
compact JSON value per line on stdout via `Writer.NDJSON().Emit(v)`
(`internal/output/envelope.go`'s `NDJSONWriter`). Unlike the `--json`
envelope, NDJSON lines are always **compact**, never indented — an
indented value could itself contain a newline, which would break the
one-record-per-line contract a downstream `jq`/line-reader depends on.
Each line is independently valid JSON; there is no enclosing array and no
trailing comma between lines.

```
{"name":"a","count":1}
{"name":"b","count":2}
```

## `NO_COLOR` / `--no-color` precedence

Highest-precedence match wins; color is disabled by ANY of:

1. `--no-color` flag set.
2. `NO_COLOR` environment variable **present**, regardless of its value
   (including empty) — per the [no-color.org](https://no-color.org)
   convention.
3. `TERM=dumb`.
4. stdout is not a terminal (see the TTY matrix above).

Color is allowed only when none of the above apply. `--no-color` is a
persistent flag registered on the root command in `cmd/cascade/main.go`
(not `root.go`'s `GlobalFlags` struct — see that file's `noColorFlag` doc
comment for why: `D/S-06.T1` and `D/S-06.T5` have disjoint `files_scope`,
and a package-level flag target works identically regardless of which file
in `package main` declares it).

## Exit-code table

Owned entirely by `pkg/cascade` (`R-14.2`/`R-14.3`) — see
[error-taxonomy.md § CLI exit codes](reference/error-taxonomy.md#cli-exit-codes)
for the full table. `internal/output.ExitCode(err)` is a direct,
zero-logic wrapper over `cascade.ExitCode(err)`; `internal/output` defines
no exit-code constants or Kind→code table of its own; `pkg/cascade`'s own
tests (`TestExitCodeTable_MatchesR143`,
`TestTaxonomyTablesTotalAndNonOverlapping`) already prove the table
exhaustive and collision-free, and `internal/output`'s
`TestExitCodeTable` proves the delegation itself stays total over every
kind rather than re-deriving the table's content a second time.

`cmd/cascade/main.go` calls `output.ExitCode(err)` exactly once, at the
boundary to `os.Exit` — the same discipline `error-taxonomy.md`'s
[Consumer rules](reference/error-taxonomy.md#consumer-rules-for-b-and-d)
section already documents for any `cmd/` composition root.

## Non-interactive equivalents (automation parity, `08-INIT-CONFIG-SPEC` §3)

Every interactive output mode has a declared non-interactive equivalent —
no cascade output behavior is reachable only through a live terminal:

| Interactive behavior | Non-interactive equivalent |
|---|---|
| Human-readable text | `--json` (versioned envelope, this document) |
| Colored output | `NO_COLOR=1` or `--no-color` (color suppression) |
| Progress spinners/counters | Automatic: suppressed whenever stdout is not a terminal (no flag needed — see the TTY matrix) |
| Increased terminal chatter | `-v`/`--verbose` (works identically piped or interactive) |
| Reduced terminal chatter | `-q`/`--quiet` (works identically piped or interactive) |

## Consumer rules

- Never write to `os.Stdout`/`os.Stderr` from `cmd/` code. Hold an
  `internal/output.Writer` (built once, at the composition root, via
  `output.NewDefault`) and call its methods instead.
- Use `Result(data)` for a command's final success payload — it dispatches
  to the JSON envelope or `Println` based on `Mode().JSON`, so command
  code never branches on the flag itself.
- Use `Fail(err)` for a command's final error, not a hand-rolled
  `fmt.Fprintln`/envelope construction — it applies the stdout/stderr
  routing decision (see above) consistently.
- Use `NDJSON()` only for genuinely streaming output (multiple records over
  time); a single result is always `Result`, never a one-line NDJSON
  stream.
- `Progress`/`Warn`/`Debug` are diagnostic-only: never put data a script
  might need to parse into any of the three — data belongs in `Result` or
  an `NDJSON().Emit` call.
