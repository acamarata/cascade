package plugins

// Purpose (this file): dbEventBus -- the lazy, real *events.Bus opener
//   cascadepa_install_events.go's busEventPublisher wraps. Split out of
//   cascadepa_install_wiring.go (which used to declare it) purely to keep
//   that file under the 300-line cap once ConfirmGate/EventBus grew real
//   implementations.
// Inputs: none at construction; every bus open is deferred to first
//   Publish call.
// Outputs: one events.Bus.Publish call per invocation, against the
//   daemon's own cascade.db.
// Constraints: shared (a *sharedCascadeStore) is REQUIRED: dbEventBus no
//   longer opens its own sqlitestore.Driver -- see
//   cascadepa_install_shared_store.go's header for why a second,
//   independent Open against the SAME cascade.db path is refused
//   (providers/sqlite.Open's own per-path exclusive flock).
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// dbEventBus lazily opens a real *events.Bus against the daemon's own
// cascade.db, via the shared store every install-flow adapter points at.
type dbEventBus struct {
	shared *sharedCascadeStore
	clock  runtime.Clock

	once    sync.Once
	onceErr error
	bus     *events.Bus
}

func newDBEventBus(shared *sharedCascadeStore, clock runtime.Clock) *dbEventBus {
	return &dbEventBus{shared: shared, clock: clock}
}

// Publish implements installEventPublisher, lazily opening the real bus on
// first use so importing this package never touches the environment.
func (d *dbEventBus) Publish(ctx context.Context, namespace string, kind events.EventKind,
	source string, payload []byte) (events.Event, error) {
	d.once.Do(func() {
		store, err := d.shared.open(ctx)
		if err != nil {
			d.onceErr = err
			return
		}
		d.bus = events.New(store, d.clock)
	})
	if d.onceErr != nil {
		return events.Event{}, d.onceErr
	}
	return d.bus.Publish(ctx, namespace, kind, source, payload)
}

// Close stops the bus's own delivery goroutines. It does NOT close the
// shared store (shared.Close does -- the store's lifetime belongs to
// whoever constructed the sharedCascadeStore, never to this bus alone).
// Safe to call on a dbEventBus that never published anything.
func (d *dbEventBus) Close() error {
	if d.bus == nil {
		return nil
	}
	return d.bus.Close()
}
