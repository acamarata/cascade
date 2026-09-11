package daemon

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestRegisterFleetJournalHandler_MountsBothMethods proves
// RegisterFleetJournalHandler actually mounts fleet.journal_show and
// fleet.journal_replay on the registry it is given, driving the real
// registry.Registered check rather than asserting on the function's
// return value alone (RegisterFleetJournalHandler returns nothing to
// assert on).
func TestRegisterFleetJournalHandler_MountsBothMethods(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterFleetJournalHandler(registry, storetest.NewMemStore(), runtime.NewSystemClock())

	if !registry.Registered(journal.MethodShow) {
		t.Errorf("registry.Registered(%q) = false, want true", journal.MethodShow)
	}
	if !registry.Registered(journal.MethodReplay) {
		t.Errorf("registry.Registered(%q) = false, want true", journal.MethodReplay)
	}
}

// TestRegisterFleetJournalHandler_NilStoreRegistersNothing proves the
// documented nil-store degradation: a nil store leaves both methods
// unregistered rather than constructing a journal.SQLiteStore over a
// store that does not exist.
func TestRegisterFleetJournalHandler_NilStoreRegistersNothing(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterFleetJournalHandler(registry, nil, runtime.NewSystemClock())

	if registry.Registered(journal.MethodShow) {
		t.Errorf("registry.Registered(%q) = true with a nil store, want false", journal.MethodShow)
	}
	if registry.Registered(journal.MethodReplay) {
		t.Errorf("registry.Registered(%q) = true with a nil store, want false", journal.MethodReplay)
	}
}
