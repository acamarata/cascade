# Platform baseline repair — Windows build and the cmd/cascade coverage ratchet

Ticket: `P1-E45-W10-S88-T2`. Audit findings F1 and F2
(`.claude/reports/2026-09-17-p1-audit-1.md`, items A1-01, A1-02, A1-43, A1-44).

Two symptoms, one release blocker: HEAD did not build on Windows, and the
`cmd/cascade` coverage ratchet had regressed below its committed baseline.

## F1 — `undefined: wireChatHandlers` on Windows

### What was wrong

`cmd/cascade/plugin_rpc.go` is untagged and calls `wireChatHandlers`. The
only implementation lived in `cmd/cascade/daemon_unix_chat.go`, which
carried `//go:build !windows`. Windows CI failed at compile:

```
cmd\cascade\plugin_rpc.go:87:12: undefined: wireChatHandlers
```

The tag was inherited, not reasoned: the file was written beside the other
`daemon_unix_*` composition files and took their build constraint with it.

### The repair

The file is now `cmd/cascade/chat_wiring.go` and carries **no build tag**.
Nothing in it is platform-specific — sqlite, the migration ledger, the
conversation adapter and the RPC registry all compile everywhere. That is
the same treatment its two siblings in `plugin_rpc.go`
(`RegisterRecallIndexHandler`, `wirePluginAddHandler`) already had.

### What the repair did NOT do

**It did not expose a daemon on Windows.** Windows is tier-2: no daemon, no
socket, no RPC server. `daemon_windows.go` refuses every daemon verb and
`DaemonSupported("windows")` is false, so nothing on that platform calls
this function. Making it compile promises no surface.

**It did not drop the one-shot path.** `cascade chat "hi"` on Windows still
runs one-shot (the TUI is the part refused, per R-16.47). It reaches the
transport and fails there because there is no daemon to reach — the
documented tier-2 limitation, unchanged by this repair and not papered
over by it.

**It did not register under embedded mode.** `conversation.ModeEmbedded`
suppresses the SSE mirror. The only caller is a daemon holding a live bus,
so the mode stays the daemon mode; the constant `chatDaemonMode` names it
so the choice is greppable and testable rather than a bare `""`.

### The regression test

`TestP1ChatPlatformBoundary` (`cmd/cascade/chat_platform_regression_test.go`),
deliberately **untagged** — a platform-tagged test could not have caught a
break whose whole nature is that one platform did not compile. Three
assertions: the registration is callable and binds all three `chat.*`
methods on whatever platform the test runs on; `DaemonSupported("windows")`
is still false while tier-1 keeps it; and the registered mode is not the
embedded mode.

### The gate that would have caught F1 before CI

`TestP1ChatPlatformBoundary` proves the repair. It does not prove the
class of defect cannot come back — it runs on one platform at a time, and
F1's whole nature is that the broken platform is the one not running.

`TestP1ChatPlatformCompilation` closes that. F1 was a **symbol table** bug,
not a logic bug, so the test asserts the symbol table's closure property
on every tuple `.github/workflows/ci.yml` builds: *every package-level name
referenced in the file set Go keeps for a platform is also declared in the
file set Go keeps for that platform.* It drives `go/build`'s own constraint
evaluator, so `//go:build` lines and the implicit `_windows.go` filename
rule are honoured exactly as the compiler honours them, and no
cross-compilation is needed to run it. Current reach: **3211 package-level
references across 4 platform tuples**.

Its scope is deliberately two identifier positions — `name(...)` and
`Name{...}` — not every `*ast.Ident`. That is what lets it run without a
type checker and never mistake a struct field or a selector for a package
reference. It is therefore a gate on the *common* shapes of `undefined:`
across a build tag, not a proof that the package compiles everywhere; the
`GOOS=windows go build` check remains the proof of compilation. The
composite-literal case was added after independent review pointed out that
the call-only version would have missed a tagged type used only as
`SomeType{…}`.

`TestP1WindowsChatRefusal` holds the other side down, so the gate above
cannot be satisfied by deleting the platform difference:

- the refusal is typed and executed here, not merely described:
  `resume.RefuseOnGOOS("windows")` returns `ErrWindowsUnsupported` with
  kind `unsupported`, and `RefuseOnGOOS("darwin")` returns nil. That
  function is deliberately not in a `_windows.go` file precisely so it is
  testable on every platform;
- every `platformDaemon*` verb darwin declares (**5** today) also has a
  declaration in a *windows-specific* file — so a new daemon verb cannot
  ship without a Windows counterpart to refuse from, and a unix lifecycle
  cannot leak onto tier-2;
- Windows still declares `wireChatHandlers` and still builds
  `builtin_plugins.go`, which is what "supported one-shot chat, no daemon"
  means as code rather than as prose.

Both gates fail closed: each asserts a non-zero count of what it checked,
because a closure check over an empty set is green and means nothing.

Mutation-proven, every half:

| Mutation | Result |
|---|---|
| Restore `//go:build !windows` on the wiring file | `GOOS=windows go build ./...` → `cmd/cascade/plugin_rpc.go:87:12: undefined: wireChatHandlers` — the exact CI error |
| The same mutation, without building for Windows | `TestP1ChatPlatformCompilation` → `windows/amd64: plugin_rpc.go references wireChatHandlers, declared only on other platforms` — caught natively, on darwin |
| Drop `adapter.RegisterHandlers(registry)` | `TestP1ChatPlatformBoundary` → `chat.append_turn is not registered on this platform: method not found` |

## F2 — the `cmd/cascade` coverage ratchet

### What was wrong

```
coverage gate: 1 violation(s) against the real tree:
  [{Package:cmd/cascade Measured:79.47127937336815 Floor:70 Baseline:80 Reason:ratchet}]
```

The composition paths landed in the preceding session — the chat wiring,
the conductor accounting wiring, the rate-card estimator and the thirty
R-14.253 result views — added statements without behaviour tests.

### The repair

Coverage was raised by **testing the new paths**, not by excluding files,
changing a coverage class, or lowering the baseline. Three test files:

| File | What it asserts |
|---|---|
| `daemon_unix_usage_test.go` | the rate-card estimator prices from the registry, reads BOTH per-token rates, and returns 0 only for the three ways a price is genuinely unknown (no registry, unregistered provider, no rate card) |
| `provider_cost_view_test.go` | `provider list`'s cost column separates "never fetched" from "fetched and has none" — the distinction that makes a zero-cost `jobs_usage` row readable (R-14.283) |
| `result_views_render_test.go` | every view added for R-14.253 renders as a sentence and leaks no `map[`, `%!` or `&{`; a failed verification never reads as verified; the quarantine listing never prints the matched text |

Measured `cmd/cascade` after the repair, from the profile itself:
**4950 / 6128 statements = 80.78%** (baseline 80, floor 70).
`TestCoverageGate_Live` run against that exact profile is green.

### On the misleading summary line

`go test ./cmd/cascade/` prints

```
ok  github.com/acamarata/cascade/cmd/cascade  62.294s  coverage: 0.1% of statements [no tests to run]
```

That `0.1% ... [no tests to run]` is a child subprocess's own summary, not
this package's profile. The gate reads the profile
(`go tool cover -func`), and so does every number in this document. The
two must never be confused: the audit called this out by name, and the
`0.1%` line is still printed by the same run that measures 80.8%.

## Evidence boundaries

- Local runs prove local behaviour. Windows **native** CI is the only
  evidence for Windows runtime behaviour; `GOOS=windows go build` proves
  compilation and nothing else, and is recorded here as exactly that.
- The authorized Intel macOS native-execution limitation stays a
  documented platform disposition. It is not counted as an executed
  runtime result.
- Coverage figures here come from the profile of the source tree at the
  commit this document lands with. A later tree needs a fresh measurement.
