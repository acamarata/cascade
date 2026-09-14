package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover ProvisionElevated's default arm — the runtime
//   value this build has no install path for. Every named tier already has
//   a test in dispatch_test.go (process, wasm, builtin, remote-disabled)
//   and the enabled-remote handshake lives in the integration-tagged file,
//   so this fills the one arm none of them reach.
// SPORT: internal/plugins dispatch-tier tests (ADD).

// TestProvisionElevated_UnknownRuntimeIsRefused proves an unrecognized
// runtime is refused by NAME, so an operator learns which value was
// rejected rather than that "something" was unsupported — and, more
// importantly, that the refusal happens before anything is committed. A
// default arm that fell through would install a plugin whose runtime
// nothing in this build knows how to start.
func TestProvisionElevated_UnknownRuntimeIsRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()

	m := mustParseManifest(t, builtinManifest)
	m.Runtime = "not-a-real-runtime"

	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err == nil {
		t.Fatal("an unknown runtime installed successfully, want a refusal")
	}
	if !strings.Contains(err.Error(), "not-a-real-runtime") {
		t.Errorf("error = %v, want it to name the unsupported runtime", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("error kind = %v (typed=%v), want %v", kind, ok, cascade.KindUnsupported)
	}

	// STORE STATE: nothing was committed for a runtime that can never
	// start, and no storage domain was claimed on its behalf.
	if _, ok, lerr := LoadMetadata(ctx, store, m.ID); lerr != nil || ok {
		t.Errorf("LoadMetadata after a refused install: ok=%v err=%v, want ok=false", ok, lerr)
	}
	if _, claimed := domains.Version(m.ID); claimed {
		t.Errorf("domains.Version(%q) was claimed by a refused install", m.ID)
	}
}
