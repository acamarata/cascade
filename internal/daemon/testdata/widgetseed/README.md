# widgetseed and the status widget fixtures

Provenance for the three artifacts the Swift widget client and the QA legs are
built against (P1-WID-08). The contract they illustrate is
`docs/widget/feed.md`. The root `testdata/README.md` is not edited here.

## widgetseed

A test-fixture program, never part of the product (it sits under `testdata/`, so
`./...` and the release build skip it). It writes providers and lanes straight
into a `providers.db` through `registry.AddProvider` and `registry.UpsertLane`.

- Every record is `Auth=key`, so no OAuth or browser flow can start against one.
- `AuthRef` is a vault-key name; nothing is stored under it. The program imports
  no secrets or vault package: `go list -deps ./internal/daemon/testdata/widgetseed`
  must not list `internal/secrets`.
- Build and use it from the repo root:

```
go build -o "$BIN/widgetseed" ./internal/daemon/testdata/widgetseed
"$BIN/widgetseed" seed --data-dir "$CASCADE_HOME/data"
"$BIN/widgetseed" lane --data-dir "$CASCADE_HOME/data" --name widget-avail --state auth-required
```

`seed` is repeatable. The five providers are `widget-avail` (available),
`widget-pool` (auth-required, pooled), `widget-limit` (exhausted, reset 2 hours
out), `widget-nolane` (no lane) and an email-named one. `lane` accepts `--state`
`available`, `constrained`, `exhausted`, `auth-required` or `unknown`, and
`--reset-seconds N`.

## fixture_status_widget_rpc.json (one level up)

Not captured from a live provider. It is the `status.widget` request and result
of the production `status-widget` registration over a registry whose lane states
were written by the real provider resolver: one captured openai 401 (a byte copy
of the in-tree literal under `internal/providers/dispatch/testdata/lane_outcome`)
for the auth-required row, a constructed anthropic 429 with `Retry-After: 7200`
for the exhausted row, and a constructed anthropic 200 for the available row. No
live 429 exists in the tree; the two constructed bodies are the dispatch tests'
own and are labelled constructed at their use site. One node, one attention item
and two active jobs are seeded through their real stores. The clock is fixed, so
the file is deterministic. Regenerate with
`CASCADE_TESTKIT_UPDATE_GOLDEN=1 go test ./cmd/cascade -run TestStatusWidgetFixtureFromEvidencePath`
(refused in CI); without the variable the test compares.

## fixture_status_widget_changed_sse.txt (one level up)

The raw bytes of one `status.widget_changed` frame (`id:` line, `data:` line, blank
line) read off a real unix socket from a daemon built as `cmd/cascade` builds it:
`NewRPCServer` over the production `RegisterStatusWidgetHandler`, the real SSE mux
and bus, served by `Run`. The test asserts the frame's base64 payload decodes to a
`WidgetSnapshot` equal to the `status.widget` result from the same daemon, and that
the same request with an `Origin` header gets HTTP 403. Rows are seeded through the
registry directly with a fixed clock. Regenerate with
`CASCADE_TESTKIT_UPDATE_GOLDEN=1 go test -tags integration ./internal/daemon -run TestStatusWidgetChangedSSEFrameOnSocket`
(refused in CI).
