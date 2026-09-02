//go:build integration

package plugins

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// Purpose: the Art.2 integration test for the Epic C acceptance ticket
//
//	(P1-E03-W1-S05-T7): initializes a real runtime config load on
//	t.TempDir(), then loads the compile-time builtin registry against the
//	real example-builtin plugin (registered via the blank-import in
//	registry_test.go, which this file shares a package and test binary
//	with) and asserts the full read surface (List/Get/Grants/
//	RPCMethodName) against that ONE real, initialized runtime.
//
// KNOWN GAP (documented, not silently dropped): the ticket contract's task
//
//	5 additionally calls for "start the event bus (C/S-04.T3) ... emit a
//	test event and verify delivery". As of this ticket's implementation,
//	internal/events/ contains only its package doc comment (doc.go) — the
//	typed persistent event bus (C/S-04.T3, weight M, a materially larger,
//	independently-dispatched ticket) has not landed in this checkout, and
//	building a bus implementation is outside this ticket's file scope
//	(internal/events/** is not among files_scope's add/change entries).
//	Fabricating a bus here to satisfy the letter of the AC would violate
//	Art.2 (no self-authored dialect standing in for the real counterpart)
//	worse than reporting the gap honestly. This test therefore proves the
//	config-load + builtin-registration half of the AC on a real,
//	initialized runtime; the event-bus half is BLOCKED pending C/S-04.T3.
//
// SPORT: internal/plugins builtin-registry integration test (ADD) —
//
//	P1-E03-W1-S05-T7.
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

	list := reg.List()
	if len(list) != 1 || list[0].Manifest.ID != "example-builtin" {
		t.Fatalf("reg.List() = %v, want exactly [example-builtin]", list)
	}
}
