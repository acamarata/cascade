# Runtime metrics (`internal/runtime`)

Status: seeded from Wave 1 (P1-E03-W1-S05-T4, internal counters/gauges +
snapshot API). Ticket contract:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-05/tickets/T-4.yaml`.

This is **internal operational instrumentation** — counts and gauges a
subsystem can register and read for its own diagnostics and for the
periodic snapshot event. It is explicitly **not** the TELEMETRY egress
system (that is a separate, opt-in, anonymized surface enumerated in
H/S-16.T1's inventory). Nothing in this package sends data anywhere by
itself; see "Where metrics go today" below for the honest current state.

## Types (`internal/runtime/metrics.go`)

- **`Counter`** — a monotonically increasing `int64`, backed by
  `sync/atomic.Int64`. `Inc()` and `Add(n)` increment; `Value()` reads.
  There is no decrement method by design.
- **`Gauge`** — a settable `int64`, also atomic. `Set(v)` replaces,
  `Add(delta)` adjusts (delta may be negative), `Value()` reads. Unlike
  `Counter` it may go negative and may decrease.
- **`MetricSample`** — one point-in-time read: `{Name string, Labels
  map[string]string, Value int64, Ts time.Time}`. `Ts` is always stamped
  from an injected `Clock`, never a bare `time.Now()`.
- **`MetricsSnapshotEvent`** — the event-bus payload: `{Samples
  []MetricSample, Ts time.Time}`.
- **`Registry`** — a thread-safe, named collection of metrics. Construct
  with `NewRegistry()`.
  - `RegisterCounter(name, labels) *Counter` / `RegisterGauge(name,
    labels) *Gauge` — **panic** on a duplicate name. This is deliberate:
    a duplicate registration is a programming error, meant to be caught
    the first time the process boots with the conflicting registration
    code paths both compiled in, not handled as a runtime condition.
  - `Get(name) (Metric, bool)` — lookup by name.
  - `Snapshot(ts time.Time) []MetricSample` — one `RLock`-held pass over
    every registered metric.

### Snapshot consistency, precisely

`Snapshot` is **structurally atomic**: no metric can be added, removed, or
renamed mid-call, because registration takes the registry's write lock for
its whole duration and `Snapshot` holds the read lock for its whole
duration too. It is **not** a cross-metric transaction: each sample's
`Value` is an independent atomic load, and `Counter`/`Gauge` deliberately
never take the registry's lock to mutate (that is the whole point of a
lock-free counter — writers never block on a snapshot in progress). Two
samples from the same `Snapshot` call may therefore reflect writes from
different instants relative to each other under concurrent load. Treat
ratios between two samples in one snapshot as approximate when writers are
active, and exact only when they are quiescent during the call.

### Cardinality

The registry places no numeric cap on the number of distinct metric
names — the ticket contract specifies none, and R-14.107 forbids inventing
one. In practice it is bounded by its access pattern rather than a limit
check: registration is a startup-time, composition-root call (per
Art.10.2 — no subsystem imports this package by `init`), driven by a fixed
set of names each subsystem compiles in, never by untrusted runtime input.
A caller that registered names sourced from unbounded external input would
be misusing the API outside its documented contract.

## Periodic emitter (`internal/runtime/metrics_emitter.go`)

`RunPeriodicEmitter(ctx, PeriodicEmitterOptions, onError)` runs a
tick-snapshot-publish loop until `ctx` is cancelled:

- **`Ticker`** — a small interface (`C() <-chan struct{}`, `Stop()`)
  abstracting the tick source so tests never sleep (R-14.136).
  `NewSystemTicker(d)` is the production implementation, backed by a real
  `time.Ticker`; its own `time.Time` tick payload is discarded — only the
  injected `Clock`'s `Now()` ever stamps a timestamp that leaves this
  package.
- **`EventBus`** — `Publish(ctx, namespace, kind, source string, payload
  []byte) error`. This is a deliberately decoupled interface, not a
  literal match of `internal/events.Bus.Publish` (which takes a typed
  `events.EventKind`, not `string`): `internal/events` already imports
  `internal/runtime` (for `Clock`), so `internal/runtime` importing
  `internal/events` back would be a cycle. The composition root
  (`cmd/cascade`, outside this ticket's scope) is expected to supply a
  thin adapter over the real `*events.Bus`.
- On each tick: `Registry.Snapshot(clock.Now())` →
  `MetricsSnapshotEvent{Samples, Ts}` → JSON-encoded (the default
  `Encode`, overridable) → `EventBus.Publish(ctx, "metrics",
  "metrics.snapshot", "runtime.metrics", payload)`.
- A `Publish` or encode error is reported to `onError` (if non-nil) and
  the loop continues — a transient bus outage does not stop metrics
  collection.
- Default interval: `DefaultMetricsInterval` = 60s, used when
  `PeriodicEmitterOptions`/`BootstrapOptions.MetricsInterval` is zero, per
  the ticket contract's "configurable interval (default 60s)".
- Delivery through the real event bus is **at-least-once**, and ordering
  is guaranteed only **within one namespace** — the same contract
  `internal/events.Bus` documents for every publisher.

## Wiring (`internal/runtime/bootstrap.go`)

`Bootstrap` always creates a `Registry` (`Runtime.Metrics`), regardless of
whether an event bus is available — subsystems may always register
counters and gauges against it. The periodic emitter goroutine is started
**only** when `BootstrapOptions.MetricsBus` is non-nil.

### Where metrics go today, honestly

As of this ticket, no composition-root caller constructs a real
`*events.Bus` and passes an adapter for it through
`BootstrapOptions.MetricsBus` — that wiring is `cmd/cascade`'s to do in a
later ticket, once a daemon-lifetime `EventBus` adapter exists. Until
then, `Runtime.Metrics` is fully usable for in-process registration and
`Snapshot()` reads, but the periodic emitter never runs in production and
nothing is published anywhere. This is stated plainly per Art.1: the
emitter code path is real and tested (`metrics_emitter_test.go`,
`bootstrap_metrics_test.go`), but it has no live sink yet.
