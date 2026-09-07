// Purpose: sibling of domain_test.go, split under R-14.117's authorized
//
//	in-scope split (Art.10.3's 300-line cap): domain isolation (a REAL
//	providers/sqlite driver, proving this package never bypasses the
//	scoping it is handed) and the event-emission tests. Moved code only,
//	no rewrites.
//
// SPORT: internal.fleet.sessions.Store/ADDED (P1-E12-W3-S24-T3).
package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestSessionDomain_WrongDomain_Isolated proves this package never opens
// its own Store: it is handed a Store already scoped to DomainSessions by
// the composition root, over a REAL providers/sqlite driver, and a
// second, independently-scoped domain cannot read what Store wrote —
// domain isolation is the driver's job (capability_isolation_test.go), and
// this test proves this package's Store never bypasses that scoping (a
// forged raw namespace string is not accepted anywhere in this package's
// API surface).
func TestSessionDomain_WrongDomain_Isolated(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.Open(ctx, t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	reg := storage.NewCapabilityRegistry(testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)))
	sessionsStore := d.Scoped(string(storage.DomainSessions), registryCheckerAdapter{reg: reg})
	other := d.Scoped(string(storage.DomainContext), registryCheckerAdapter{reg: reg})

	store := sessions.New(sessionsStore, testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), nil)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", State: "running"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_, err = other.Get(ctx, string(storage.DomainSessions), "session:s1")
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Get of sessions data = %v, want ErrDomainForbidden", err)
	}
}

// TestEventEmission_OnUpsertAndTransition proves exactly one
// fleet.sessions.changed event fires per successful Upsert/TransitionState
// call, over a REAL internal/events.Bus.
func TestEventEmission_OnUpsertAndTransition(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	store := sessions.New(storetest.NewMemStore(), clock, bus)

	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", State: "starting"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.TransitionState(ctx, "s1", "starting", "running"); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	got, err := bus.Replay(ctx, "fleet.sessions", 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events published = %d, want exactly 2 (one per successful write): %+v", len(got), got)
	}
	for _, ev := range got {
		if string(ev.Kind) != "fleet.sessions.changed" {
			t.Errorf("event kind = %q, want fleet.sessions.changed", ev.Kind)
		}
	}
}

// TestEventEmission_NilBusIsBestEffort proves a nil bus never fails a
// write — emission is allowed-fail before the bus is wired.
func TestEventEmission_NilBusIsBestEffort(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Upsert(context.Background(), sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("Upsert with nil bus must not fail: %v", err)
	}
}

// registryCheckerAdapter mirrors internal/storage/capability_test.go's own
// registryChecker helper (unexported there, so duplicated minimally here
// rather than depending on an internal test-only symbol across packages).
type registryCheckerAdapter struct{ reg *storage.CapabilityRegistry }

func (r registryCheckerAdapter) Check(ctx context.Context, src, dst string, op sqlite.CapOp) error {
	var sop storage.Op
	if op&sqlite.CapOpRead != 0 {
		sop |= storage.OpRead
	}
	if op&sqlite.CapOpWrite != 0 {
		sop |= storage.OpWrite
	}
	return r.reg.Check(ctx, storage.DomainID(src), storage.DomainID(dst), sop)
}

var _ sqlite.GrantChecker = registryCheckerAdapter{}
