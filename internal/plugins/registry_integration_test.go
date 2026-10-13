//go:build integration

package plugins

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: the Art.2 integration test for the Epic C acceptance ticket
//
//	(P1-E03-W1-S05-T7): initializes a real runtime config load on
//	t.TempDir(), then loads the compile-time builtin registry against the
//	real example-builtin plugin (registered via the blank-import in
//	registry_test.go, which this file shares a package and test binary
//	with) and asserts the full read surface (List/Get/Grants/
//	RPCMethodName) against that ONE real, initialized runtime. It also
//	starts a real internal/events.Bus, publishes a real event describing
//	the registration it just proved, and asserts real delivery through a
//	real Subscribe — completing the event-delivery half of the AC.
//
// R-14.134 (15-T0-RULINGS-R14.md): this half was previously BLOCKED and
// documented as such, because internal/events held only its package doc
// comment when P1-E03-W1-S05-T7 landed — the typed persistent event bus
// (P1-E03-W1-S04-T3) had not shipped yet. That ticket's own DoD required
// it to come back and complete this test's event-delivery half once the
// bus existed, which this file scope extension (internal/plugins'
// integration test, authorized by R-14.134 for this specific obligation)
// does: a real internal/events.Bus over a real Store, a real Publish of an
// EventKindPluginRegistered event naming the SAME builtin this test just
// loaded and asserted, and a real Subscribe proving that exact event
// arrives, in order, with no fabrication standing in for either half.
//
// The List() surface is asserted by membership, not as an exact list: other
// compile-time builtins register in this test binary, so the test checks
// that exactly one entry is example-builtin, none is the rejected
// invalid-example, and the list is sorted by Manifest.ID.
//
// SPORT: internal/plugins builtin-registry integration test (ADD) —
//
//	P1-E03-W1-S05-T7; event-delivery half completed by P1-E03-W1-S04-T3
//	per R-14.134.
func TestBuiltinRegistry_Integration_RealConfigLoadAndRegistration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path: filepath.Join(dir, "config.toml"),
	})
	if err != nil {
		t.Fatalf("runtime.Load(...) error = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatal("runtime.Load(...) returned nil *Config with a nil error")
	}

	reg := &BuiltinRegistry{}
	if err := reg.Load(); err == nil {
		t.Fatal("BuiltinRegistry.Load() error = nil, want non-nil (invalid-example from registry_test.go's init must be rejected)")
	}

	entry, ok := reg.Get("example-builtin")
	if !ok {
		t.Fatal(`reg.Get("example-builtin") ok = false, want true`)
	}
	if entry.Manifest.Schema != "cascade.plugin/v2" {
		t.Errorf("entry.Manifest.Schema = %q, want %q", entry.Manifest.Schema, "cascade.plugin/v2")
	}

	if grants := reg.Grants("example-builtin"); len(grants) != 1 || grants[0] != "read" {
		t.Errorf(`reg.Grants("example-builtin") = %v, want [read]`, grants)
	}

	const wantRPC = "plugin.example-builtin.greet"
	if got := reg.RPCMethodName("example-builtin", "greet"); got != wantRPC {
		t.Errorf("RPCMethodName(...) = %q, want %q", got, wantRPC)
	}

	assertListMembership(t, reg.List())

	assertPluginRegisteredEventDelivered(ctx, t, entry.Manifest.ID)
}

// assertListMembership asserts the List() read surface by membership, not by
// an exact one-entry list: other compile-time builtins register in the same
// test binary, so exactly one entry must carry the example-builtin ID, no
// entry may carry the rejected invalid-example ID, and List must be sorted by
// Manifest.ID.
func assertListMembership(t *testing.T, list []plugin.BuiltinRegistration) {
	t.Helper()

	found := 0
	for i, entry := range list {
		switch entry.Manifest.ID {
		case "example-builtin":
			found++
		case "invalid-example":
			t.Errorf("reg.List() holds the rejected manifest invalid-example at index %d", i)
		}
		if i > 0 && list[i-1].Manifest.ID >= entry.Manifest.ID {
			t.Errorf("reg.List() not sorted by Manifest.ID: %q before %q", list[i-1].Manifest.ID, entry.Manifest.ID)
		}
	}
	if found != 1 {
		t.Fatalf("reg.List() holds %d entries with Manifest.ID example-builtin, want exactly 1", found)
	}
}

// assertPluginRegisteredEventDelivered starts a real internal/events.Bus,
// subscribes to it, publishes a real EventKindPluginRegistered event
// naming pluginID, and asserts that exact event is really delivered — the
// R-14.134 obligation this file now discharges.
func assertPluginRegisteredEventDelivered(ctx context.Context, t *testing.T, pluginID string) {
	t.Helper()

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	const namespace = "queue"
	sub, err := bus.Subscribe(ctx, namespace, "integration-test-consumer", 1)
	if err != nil {
		t.Fatalf("Bus.Subscribe: %v", err)
	}

	published, err := bus.Publish(ctx, namespace, events.EventKindPluginRegistered, pluginID, []byte(pluginID))
	if err != nil {
		t.Fatalf("Bus.Publish: %v", err)
	}

	select {
	case delivered, ok := <-sub.Events:
		if !ok {
			t.Fatal("Bus Events channel closed before the published event arrived")
		}
		if delivered.Seq != published.Seq {
			t.Fatalf("delivered Seq = %d, want %d", delivered.Seq, published.Seq)
		}
		if delivered.Kind != events.EventKindPluginRegistered {
			t.Fatalf("delivered Kind = %q, want %q", delivered.Kind, events.EventKindPluginRegistered)
		}
		if delivered.Source != pluginID {
			t.Fatalf("delivered Source = %q, want %q", delivered.Source, pluginID)
		}
		if string(delivered.Payload) != pluginID {
			t.Fatalf("delivered Payload = %q, want %q", delivered.Payload, pluginID)
		}
	case deliveryErr := <-sub.Errs:
		t.Fatalf("Bus delivery error: %v", deliveryErr)
	}
}
