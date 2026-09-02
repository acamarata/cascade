package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Purpose: the periodic metrics emitter — Ticker abstraction, the
//   decoupled EventBus publish seam, and RunPeriodicEmitter. Split out of
//   metrics.go under R-14.117 (Art.10.3's 300-line cap; a cap-driven split
//   joins the ticket's authorized write set automatically) — this file and
//   metrics.go are one package, one ticket, moved code only.
// Inputs: a *Registry to snapshot, an injected Clock (for the event's Ts),
//   an injected Ticker (for pacing — never a real sleep in tests, per
//   R-14.136), and an injected EventBus (for publish).
// Outputs: a MetricsSnapshotEvent published once per tick until ctx is
//   cancelled.
// Constraints: no bare time.Now (02-TARGET-STRUCTURE §v1.1) — the ticker's
//   own time.Time payload is discarded; only Clock.Now ever stamps a
//   timestamp that leaves this package. See metrics.go's package-level
//   doc comment for the internal-vs-telemetry boundary statement.
// SPORT: runtime/metrics (ADD, placeholder per T-1 sport_updates).

// DefaultMetricsInterval is the periodic emitter's tick interval when
// PeriodicEmitterOptions.Interval is left at its zero value, per the
// contract's "configurable interval (default 60s)".
const DefaultMetricsInterval = 60 * time.Second

// Ticker abstracts periodic notification so the emitter never blocks on a
// real sleep in tests (R-14.136 — a suite that sleeps is a flake every
// caller inherits). Production code uses NewSystemTicker (backed by
// time.NewTicker); tests inject a fake that fires on demand, confined to
// metrics_test.go per Art.1.
type Ticker interface {
	// C returns the channel that receives a tick.
	C() <-chan struct{}
	// Stop releases the ticker's resources. Safe to call more than once.
	Stop()
}

// systemTicker is the production Ticker, backed by a real time.Ticker. Its
// own tick payload (a time.Time) is intentionally discarded — the emitter
// stamps MetricsSnapshotEvent.Ts from the injected Clock, never from the
// ticker's own wall-clock read, so the forbidigo bare-time.Now rule has
// nothing to catch here and Art.7.3's determinism story stays intact:
// only Clock.Now is ever read for a timestamp that ends up in output.
type systemTicker struct {
	t    *time.Ticker
	c    chan struct{}
	stop chan struct{}
	once sync.Once
}

// NewSystemTicker returns the production Ticker, firing every d (d must be
// positive). Only production entrypoints (bootstrap.go and above) should
// call this; tests must inject a fake Ticker instead.
func NewSystemTicker(d time.Duration) Ticker {
	st := &systemTicker{t: time.NewTicker(d), c: make(chan struct{}), stop: make(chan struct{})}
	go st.pump()
	return st
}

// pump forwards each real tick onto st.c as an empty struct, decoupling
// the emitter from time.Time so it never touches a wall-clock value
// directly. It exits when Stop closes st.stop.
func (st *systemTicker) pump() {
	for {
		select {
		case <-st.t.C:
			select {
			case st.c <- struct{}{}:
			case <-st.stop:
				return
			}
		case <-st.stop:
			return
		}
	}
}

// C returns the tick channel.
func (st *systemTicker) C() <-chan struct{} { return st.c }

// Stop stops the underlying time.Ticker and the pump goroutine. Safe to
// call more than once.
func (st *systemTicker) Stop() {
	st.once.Do(func() {
		st.t.Stop()
		close(st.stop)
	})
}

// EventBus is the minimal publish surface the periodic emitter needs.
// internal/events.Bus already depends on internal/runtime (for its Clock
// injection — see internal/events/bus.go), so internal/runtime cannot
// import internal/events without creating an import cycle; EventBus is
// therefore a deliberately decoupled, structurally-typed seam rather than
// a literal match of Bus.Publish's signature (which takes an
// events.EventKind, not a string). The composition root (cmd/cascade, out
// of this ticket's scope) is expected to supply a thin adapter over the
// real *events.Bus. See docs/developer/runtime.md for the wiring note and
// CONTRACT-DEVIATIONS in this ticket's journal for why this differs from
// the contract's literal "the C/S-04.T3 event bus" phrasing.
type EventBus interface {
	// Publish sends one event of kind on namespace, tagged with source,
	// carrying payload. It returns an error if delivery could not be
	// accepted (never a panic). Delivery is at-least-once and ordering
	// is guaranteed only within one namespace, matching internal/events'
	// own documented contract.
	Publish(ctx context.Context, namespace, kind, source string, payload []byte) error
}

// PeriodicEmitterOptions configures RunPeriodicEmitter. Registry, Clock,
// Ticker, and Bus are required (a nil field is a caller programming
// error); Interval defaults to DefaultMetricsInterval when zero;
// Namespace and Source default to "metrics" and "runtime.metrics" when
// empty; Encode defaults to encoding the snapshot as JSON when nil.
type PeriodicEmitterOptions struct {
	Registry  *Registry
	Clock     Clock
	Ticker    Ticker
	Bus       EventBus
	Namespace string
	Source    string
	Encode    func(MetricsSnapshotEvent) ([]byte, error)
}

// RunPeriodicEmitter runs the periodic snapshot-and-publish loop until ctx
// is cancelled, then stops opts.Ticker and returns. It is meant to be
// called in its own goroutine (see bootstrap.go). On every tick it takes
// one Registry.Snapshot, wraps it in a MetricsSnapshotEvent stamped from
// opts.Clock, encodes it, and publishes it through opts.Bus. A publish or
// encode error is swallowed after being reported via callback if one is
// set (errors must never crash the emitter loop — a transient bus outage
// should not take down metrics collection) — RunPeriodicEmitter has no
// return value because ctx cancellation, not an error, is its only exit
// path.
func RunPeriodicEmitter(ctx context.Context, opts PeriodicEmitterOptions, onError func(error)) {
	if opts.Ticker != nil {
		defer opts.Ticker.Stop()
	}
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "metrics"
	}
	source := opts.Source
	if source == "" {
		source = "runtime.metrics"
	}
	encode := opts.Encode
	if encode == nil {
		encode = encodeMetricsSnapshotJSON
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-opts.Ticker.C():
			ts := opts.Clock.Now()
			ev := MetricsSnapshotEvent{Samples: opts.Registry.Snapshot(ts), Ts: ts}
			payload, err := encode(ev)
			if err != nil {
				if onError != nil {
					onError(err)
				}
				continue
			}
			if err := opts.Bus.Publish(ctx, namespace, "metrics.snapshot", source, payload); err != nil {
				if onError != nil {
					onError(err)
				}
			}
		}
	}
}

// encodeMetricsSnapshotJSON is the default Encode function for
// PeriodicEmitterOptions: a plain JSON encoding of MetricsSnapshotEvent,
// using only the standard library (no new runtime dependency).
func encodeMetricsSnapshotJSON(ev MetricsSnapshotEvent) ([]byte, error) {
	return json.Marshal(ev)
}
