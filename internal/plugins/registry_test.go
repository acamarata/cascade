package plugins

import (
	"context"
	"testing"

	// Blank-import triggers example-builtin's init(), which calls
	// plugin.RegisterBuiltin — the compile-time registration this whole
	// test suite exercises. registry.go (this package, under test) itself
	// now imports plugins/codex and plugins/opencode to wire their real
	// generators (P1-E16-W4-S34-T3, P1-E16-W4-S35-T1), and
	// cascadepa_wiring.go (also this package) imports plugins/cascade-pa
	// to wire its real client adapter, so this test binary also observes
	// cascade-codex, cascade-opencode, and cascade-pa registered — see
	// TestBuiltinRegistry_List's updated count below. cascade-pa's
	// manifest declares no commands (its "chat" command is mounted
	// directly on root, not through this registry — see
	// FIX-manifest-collision-and-conductor-seam), so it now loads
	// validly instead of being rejected on a name collision.
	_ "github.com/acamarata/cascade/plugins/examples/example-builtin"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: unit tests for the host-side BuiltinRegistry (registry.go):
//   Load's accept/reject behavior, Get/List/Grants lookups, RPCMethodName's
//   naming pattern, and NewCobraCommand's real descriptor construction.
// Constraints: fakeHandlers below is a BuiltinHandlers test fake confined
//   to this _test.go file (Art.1); the invalid-example registration in
//   this file's init() exercises Load's rejection path — it is registered
//   via the package-global plugin.RegisterBuiltin/Builtins pair, so it
//   MUST happen in an init() (guaranteed to run before any Test function,
//   regardless of -shuffle=on test-execution order) rather than inside a
//   test body, where registration order relative to the first
//   plugin.Builtins() call would otherwise be nondeterministic.
// SPORT: internal/plugins builtin-registry tests (ADD) — P1-E03-W1-S05-T7.

// fakeHandlers is a minimal BuiltinHandlers test fake, confined to this
// _test.go file per the ticket contract.
type fakeHandlers struct{}

func (fakeHandlers) DispatchTool(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (fakeHandlers) DispatchIntent(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}
func (fakeHandlers) RunCommand(context.Context, string, []string) error { return nil }

// init registers one deliberately INVALID manifest (schema field wrong —
// rule R1) alongside the real example-builtin registration, so
// TestBuiltinRegistry_Load below can exercise Load's rejection path
// against the SAME process-global plugin.Builtins() snapshot every test in
// this file (and package) observes. See the package doc comment for why
// this lives in init(), not a test body.
func init() {
	plugin.RegisterBuiltin(plugin.Manifest{
		ID:          "invalid-example",
		Name:        "Invalid Example",
		Schema:      "cascade.plugin/v1", // wrong on purpose — triggers rule R1
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
	}, fakeHandlers{})
}

// loadedRegistry returns a *BuiltinRegistry that has already called Load
// once, for tests that only care about post-Load lookups.
func loadedRegistry(t *testing.T) *BuiltinRegistry {
	t.Helper()
	r := &BuiltinRegistry{}
	_ = r.Load() // error asserted separately by TestBuiltinRegistry_Load
	return r
}

func TestBuiltinRegistry_Load(t *testing.T) {
	r := &BuiltinRegistry{}
	err := r.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil (invalid-example must be rejected)")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Load() error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}

	// The valid manifest is still indexed despite the rejection elsewhere
	// in the same Load call.
	if _, ok := r.Get("example-builtin"); !ok {
		t.Error(`Get("example-builtin") ok = false, want true`)
	}
	// The invalid manifest must never appear.
	if _, ok := r.Get("invalid-example"); ok {
		t.Error(`Get("invalid-example") ok = true, want false (rejected by Validate)`)
	}
}

func TestBuiltinRegistry_GetUnknown(t *testing.T) {
	r := loadedRegistry(t)
	if _, ok := r.Get("nonexistent"); ok {
		t.Error(`Get("nonexistent") ok = true, want false`)
	}
}

func TestBuiltinRegistry_List(t *testing.T) {
	r := loadedRegistry(t)
	list := r.List()
	wantIDs := []string{"cascade-codex", "cascade-opencode", "cascade-pa", "example-builtin"}
	if len(list) != len(wantIDs) {
		t.Fatalf("List() len = %d, want %d (%v)", len(list), len(wantIDs), wantIDs)
	}
	for i, id := range wantIDs {
		if list[i].Manifest.ID != id {
			t.Errorf("List()[%d].Manifest.ID = %q, want %q (List sorts by manifest id)", i, list[i].Manifest.ID, id)
		}
	}
}

func TestBuiltinRegistry_Grants(t *testing.T) {
	r := loadedRegistry(t)
	grants := r.Grants("example-builtin")
	if len(grants) != 1 || grants[0] != "read" {
		t.Fatalf("Grants(%q) = %v, want [read]", "example-builtin", grants)
	}
	if got := r.Grants("nonexistent"); got != nil {
		t.Errorf("Grants(nonexistent) = %v, want nil", got)
	}
}

func TestBuiltinRegistry_RPCMethodName(t *testing.T) {
	r := &BuiltinRegistry{}
	cases := []struct {
		pluginID, command, want string
	}{
		{"example-builtin", "greet", "plugin.example-builtin.greet"},
		{"cascade-pbd", "run", "plugin.cascade-pbd.run"},
		{"a", "b", "plugin.a.b"},
		{"my-plugin", "do-thing", "plugin.my-plugin.do-thing"},
		{"x1", "y2", "plugin.x1.y2"},
	}
	for _, tc := range cases {
		if got := r.RPCMethodName(tc.pluginID, tc.command); got != tc.want {
			t.Errorf("RPCMethodName(%q, %q) = %q, want %q", tc.pluginID, tc.command, got, tc.want)
		}
	}
}

func TestBuiltinRegistry_NewCobraCommand(t *testing.T) {
	r := loadedRegistry(t)

	cmd, ok := r.NewCobraCommand("example-builtin", "greet")
	if !ok {
		t.Fatal("NewCobraCommand(example-builtin, greet) ok = false, want true")
	}
	if cmd.Name() != "greet" {
		t.Errorf("cmd.Name() = %q, want %q", cmd.Name(), "greet")
	}
	if cmd.RunE == nil {
		t.Fatal("cmd.RunE is nil, want a non-nil RunE")
	}

	// The RunE closure genuinely dispatches into the real BuiltinHandlers
	// (Art.1 — not a placeholder): calling it must not error and must not
	// panic.
	if err := cmd.RunE(cmd, []string{"tester"}); err != nil {
		t.Errorf("cmd.RunE(...) error = %v, want nil", err)
	}
}

func TestBuiltinRegistry_NewCobraCommand_Unknown(t *testing.T) {
	r := loadedRegistry(t)

	if _, ok := r.NewCobraCommand("nonexistent-plugin", "greet"); ok {
		t.Error("NewCobraCommand(nonexistent-plugin, greet) ok = true, want false")
	}
	if _, ok := r.NewCobraCommand("example-builtin", "nonexistent-command"); ok {
		t.Error("NewCobraCommand(example-builtin, nonexistent-command) ok = true, want false")
	}
}
