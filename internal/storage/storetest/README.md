# internal/storage/storetest — storage conformance suite

Per this ticket's (P1-E02-W1-S02-T1) `docs_updates`, this is the usage guide
for the portable compliance suite driver authors run against every
`pkg/provider` storage-family implementation. A driver that passes its
family's `Run*Tests` function is correct by construction against that
interface's contract — including its error paths, not just its happy path.

## Entry points

| Function | Family | Factory type | Covers |
|---|---|---|---|
| `RunStoreTests` | `provider.Store` | `StoreFactory` | Get/Put/Delete/Scan, key-not-found, Tx commit + conflict, conditional-update (CAS) |
| `RunVectorStoreTests` | `provider.VectorStore` | `VectorStoreFactory` | Upsert/Query/Delete/Count/Namespaces, all namespace-scoped (R-14.4 canonical set) |
| `RunBlobStoreTests` | `provider.BlobStore` | `BlobStoreFactory` | Put/Get/Delete/Exists, content-addressed BLAKE3 hashing, key-not-found, idempotent Put |
| `RunCacheTests` | `provider.Cache` | `CacheFactory` | Get/Set/Evict/Flush, LRU-compatible eviction behavior |
| `RunQueueTests` | `provider.Queue` | `QueueFactory` | Enqueue/Dequeue/Ack/Nack, at-least-once redelivery, ack-timeout, enqueue-overflow (see below) |

Each function takes `*testing.T` and a factory (`func(t *testing.T) provider.X`)
and runs its coverage as named `t.Run` sub-tests, so failures report with a
readable path (e.g. `--- FAIL: TestMyDriver/AckTimeout`).

## Wiring a driver in

A driver author calls the relevant `Run*Tests` function from their own
`_test.go` file, passing a factory that constructs one fresh, empty driver
instance per call:

```go
package mydriver_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestMyStore_Conformance(t *testing.T) {
	storetest.RunStoreTests(t, func(t *testing.T) provider.Store {
		t.Helper()
		return newMyStore(t) // fresh, empty instance
	})
}
```

The factory is called once per top-level `Run` and again for every sub-test
that needs isolation from a previous sub-test's writes — never assume a
shared or pre-seeded backing store. If constructing a fresh instance needs
teardown (temp dir, connection, container), register it with `t.Cleanup`
inside the factory.

`internal/storage/storetest/memstore.go` (`storetest.NewMemStore`) is a
real, goroutine-safe in-memory `provider.Store` — including full Tx and
conditional-update/CAS semantics — that itself passes `RunStoreTests`. It is
not a suite dependency; it is the shared test substrate other tickets build
on (S-02.T4's Cache/Queue implementations, S-03.T4's localvector driver),
and a second worked example of a factory in
`storetest_test.go:TestRunStoreTests_MemStore`.

## The BoundedQueue upcast

`RunQueueTests` always exercises the ack-timeout error path (every driver
has a visibility timeout, so every driver can redeliver a stale-receipt
message). Enqueue-overflow is different: a driver with no fixed capacity
(an unbounded in-memory reference, for example) has nothing to overflow, so
the suite cannot assume every driver supports it.

Instead, `RunQueueTests` type-asserts the driver returned by the factory
against:

```go
type BoundedQueue interface {
	Capacity(namespace string) int
}
```

If the driver implements `BoundedQueue` and `Capacity` returns a positive
number for the test namespace, the suite fills the namespace to capacity
and asserts that one more `Enqueue` fails with `cascade.KindQuotaExhausted`
(R-14.125 — overflow means the backend is healthy but out of capacity,
which calls for backpressure, not the "backend unreachable, retry" meaning
of `cascade.KindUnavailable`). If the driver does not implement
`BoundedQueue`, or `Capacity` returns zero or negative, the sub-test calls
`t.Skip` rather than failing — an unbounded driver is not out of
conformance for lacking a capacity limit.

A driver only needs to implement `BoundedQueue` if it actually enforces a
capacity; adding a fake `Capacity` that returns a positive number commits
the driver to real overflow behavior under this suite.

## Deterministic ack-timeout: WithQueueClock (R-14.136)

`RunQueueTests` always exercises the ack-timeout error path, and by default
it does so by Dequeuing with a near-zero visibility timeout and polling
real elapsed time until redelivery happens. That is real-time-dependent —
a flake every future `Queue` driver inherits, and the cost grows with each
driver (R-14.136).

A driver whose implementation takes an injected clock (per
`internal/runtime.Clock` or the structurally-identical
`internal/testkit.Clock`, R-14.126) can get a fully deterministic run by
passing that SAME clock instance to `RunQueueTests`:

```go
clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
storetest.RunQueueTests(t, func(t *testing.T) provider.Queue {
	t.Helper()
	return mydriver.New(store, clock, mydriver.Config{})
}, storetest.WithQueueClock(clock))
```

With a clock supplied, the AckTimeout case Dequeues with a normal
visibility timeout, advances the clock directly past it
(`clock.Advance(...)`), and asserts redelivery on the very next `Dequeue`
call — no sleep, no poll, and the case runs as fast as any other.

**Fallback for a driver with no clock seam:** `WithQueueClock` is optional.
A driver whose implementation does not accept a clock (reads `time.Now()`
internally, or wraps a backend that does) cannot have its expiry advanced
by the suite, so `RunQueueTests` falls back to today's real-time poll —
existing callers that pass no options are unaffected and keep compiling
and passing unmodified. A driver author who wants the deterministic,
poll-free path must expose a clock seam (accept a
`runtime.Clock`/`testkit.Clock` at construction) and pass that same
instance via `WithQueueClock`; passing an unrelated clock instance does
NOT degrade gracefully to the fallback — the driver never observes the
advance, so the case fails outright, since that mismatch is a test-setup
bug worth surfacing rather than silently masking as a working case.

## What the suite does and does not guarantee

Passing a family's `Run*Tests` proves the driver satisfies that interface's
documented contract as exercised by the suite: correct results on the
happy path, and the taxonomy error `Kind` the interface's godoc promises on
each documented error path (key-not-found, tx-conflict, enqueue-overflow,
ack-timeout, etc. — see each `pkg/provider/*.go` interface's method
comments for the authoritative list per family).

It does **not** guarantee:

- **Performance or scale.** The suite uses small fixed inputs sized for
  fast, deterministic CI runs, not load or stress testing.
- **Concurrency safety beyond what a family's contract requires.** Run the
  suite under `go test -race`; a driver-specific concurrency stress test is
  the driver's own responsibility if its contract promises more than the
  suite exercises.
- **Persistence across process restarts, replication, or any durability
  guarantee beyond what the interface itself documents** — the suite only
  ever observes one live driver instance within one test process.
- **Every error condition a real backend can produce.** The suite covers
  the error paths named in each interface's godoc (this ticket's
  `checks`: key-not-found, tx-conflict, enqueue-overflow, ack-timeout). A
  driver-specific backend failure mode outside that list (e.g. a network
  partition against a remote store) is out of scope for this suite and
  belongs in the driver's own tests.

## Provenance

Every suite function and its test-only doubles (`doubles_test.go`) are
hand-authored against the `pkg/provider` interface contracts declared by
this same ticket (P1-E02-W1-S02-T1) — not derived from an external fixture
or golden corpus, so there is no version/date provenance table to record
here (contrast `pkg/plugin/testdata/README.md`, which documents fixtures
harvested from a real external catalog). The one piece of recorded
provenance that matters here is the ruling trail: R-14.4 fixes the
`VectorStore` canonical method set this suite enforces, and R-14.125 fixes
the enqueue-overflow error `Kind` this suite asserts — both cited inline at
the exact assertion they govern (`vector_suite.go`, `queue_suite.go`) so a
future edit to either can be checked against the ruling it implements.
