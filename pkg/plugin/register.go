package plugin

import (
	"context"
	"sort"
	"sync"
)

// Purpose: the compile-time builtin-plugin registration surface — the
//   public SDK entry point a builtin plugin package calls from its own
//   init() to make itself known to the host, and the single read-back the
//   host calls exactly once at boot.
// Inputs: a Manifest (already expected to satisfy Validate — RegisterBuiltin
//   does not itself validate; internal/plugins.BuiltinRegistry.Load owns
//   rejection) and a BuiltinHandlers implementation.
// Outputs: BuiltinRegistration entries, snapshotted and frozen the first
//   time Builtins() is called.
// Constraints: pkg/plugin never imports internal/ (Art.10.2); no bare
//   fmt.Errorf/errors.New (boundary lint — this file mints no errors at
//   all, so the constraint is met vacuously); ctx-first on every
//   dispatch-shaped method (02-TARGET-STRUCTURE.md §v1.1); the registry
//   must be safe under concurrent registration with deterministic ordering
//   (Art.11 — a map/slice-order flake is never acceptable), so Builtins()
//   sorts its snapshot by manifest id rather than preserving raw
//   registration order.
// SPORT: pkg/plugin builtin-registration-api (ADD) — P1-E03-W1-S05-T7.

// BuiltinHandlers is the dispatch surface a builtin plugin package
// implements and hands to RegisterBuiltin. The host calls these methods
// once the plugin is loaded and (per its Grants) enabled: DispatchTool and
// DispatchIntent service the plugin's declared ToolSpec/IntentSpec
// entries, and RunCommand services its declared CommandSpec entries. Every
// method is ctx-first (02-TARGET-STRUCTURE.md §v1.1).
type BuiltinHandlers interface {
	// DispatchTool invokes the tool named name (matching one of the
	// plugin's Manifest.Provides.Tools entries) with input and returns its
	// output, or a non-nil error if name is unrecognized or dispatch
	// fails.
	DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error)
	// DispatchIntent resolves the intent named name (matching one of the
	// plugin's Manifest.Provides.Intents entries) with input and returns
	// its output, or a non-nil error if name is unrecognized or dispatch
	// fails.
	DispatchIntent(ctx context.Context, name string, input []byte) ([]byte, error)
	// RunCommand executes the CLI command named name (matching one of the
	// plugin's Manifest.Provides.Commands entries) with the given
	// arguments, or returns a non-nil error if name is unrecognized or
	// execution fails.
	RunCommand(ctx context.Context, name string, args []string) error
}

// BuiltinRegistration is one compile-time-registered builtin plugin: its
// manifest, its handler implementation, and the grants conferred on it at
// registration time.
type BuiltinRegistration struct {
	// Manifest is the plugin's cascade.plugin/v2 manifest, as passed to
	// RegisterBuiltin. It is NOT validated by RegisterBuiltin itself —
	// internal/plugins.BuiltinRegistry.Load runs Validate against every
	// entry and rejects invalid manifests at load time.
	Manifest Manifest
	// Handlers is the plugin's BuiltinHandlers implementation.
	Handlers BuiltinHandlers
	// Grants is the set of capability grants conferred on this plugin.
	// RegisterBuiltin always seeds this to []string{"read"} — W1 confers
	// no other grant; the full capability-policy engine (I/S-17.T1) is out
	// of this ticket's scope.
	Grants []string
}

// builtinRegistry is the mutable backing store RegisterBuiltin/Builtins
// operate on, extracted as its own type (rather than bare package
// variables) so register_test.go can exercise freeze, double-registration,
// and ordering semantics against private instances without perturbing
// defaultBuiltinRegistry — the single process-global instance every real
// builtin plugin package's init() registers into via RegisterBuiltin.
type builtinRegistry struct {
	mu      sync.Mutex
	once    sync.Once
	entries []BuiltinRegistration
	frozen  []BuiltinRegistration
	closed  bool
}

// register appends a new entry, seeding Grants to ["read"]. A call after
// the registry has been frozen by snapshot (see below) is a deterministic
// no-op: the entry is silently dropped, never appears in a later
// snapshot(), and register never panics. This is deliberate, not an
// oversight — init() runs strictly before any other code in a Go program
// (the Go language spec's init-ordering guarantee), so a "late"
// RegisterBuiltin call after the host has already read Builtins() can only
// originate from test code or a bug; dropping it keeps a compile-time-
// registered production binary crash-free rather than panicking deep
// inside an unrelated caller's boot sequence.
func (r *builtinRegistry) register(m Manifest, h BuiltinHandlers) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.entries = append(r.entries, BuiltinRegistration{
		Manifest: m,
		Handlers: h,
		Grants:   []string{"read"},
	})
}

// snapshot computes and caches the frozen, sorted view of every entry
// registered before the FIRST call to snapshot. sync.Once guarantees the
// computation runs exactly once and that every caller (concurrent or not)
// observes the same frozen slice thereafter — the registry is immutable at
// runtime from this point on, per the ticket contract. Sorting by manifest
// id (rather than raw append order) makes the returned order deterministic
// regardless of init() call order, goroutine scheduling, or -shuffle=on
// test execution order (Art.11).
func (r *builtinRegistry) snapshot() []BuiltinRegistration {
	r.once.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		out := make([]BuiltinRegistration, len(r.entries))
		copy(out, r.entries)
		sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
		r.frozen = out
	})
	return r.frozen
}

// defaultBuiltinRegistry is the single process-global registry every real
// builtin plugin package's init() registers into via the package-level
// RegisterBuiltin/Builtins functions below.
var defaultBuiltinRegistry builtinRegistry

// RegisterBuiltin registers a builtin plugin's manifest and handler
// implementation with the process-global registry. It is intended to be
// called from a builtin plugin package's own init() function — see
// plugins/examples/example-builtin/plugin.go for the canonical pattern —
// so that a compile-time blank-import of the plugin package (as the
// binary composition root, or a test, performs) is sufficient to make the
// plugin known to the host with zero runtime configuration.
//
// Grants is always seeded to []string{"read"}: W1 confers no other grant
// automatically.
//
// RegisterBuiltin is safe to call concurrently. A call made after Builtins
// has already been invoked once (anywhere in the process) is a no-op — see
// builtinRegistry.register's doc comment for why that is the correct,
// deterministic behavior rather than a panic.
func RegisterBuiltin(m Manifest, h BuiltinHandlers) {
	defaultBuiltinRegistry.register(m, h)
}

// Builtins returns a snapshot of every builtin plugin registered via
// RegisterBuiltin, sorted by manifest id for deterministic ordering. The
// FIRST call freezes the registry: this exact slice (never recomputed) is
// what every subsequent call returns, and every RegisterBuiltin call after
// this point is silently dropped. Production callers (the daemon or an
// embedded runtime's boot sequence) call this exactly once; the module
// path in the ticket contract's HOW section describes cmd/cascade doing
// exactly that via blank-importing every first-party builtin package.
func Builtins() []BuiltinRegistration {
	return defaultBuiltinRegistry.snapshot()
}
