package plugins

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: the host-side registry consuming pkg/plugin's compile-time
//   builtin registration surface: validates every registered manifest
//   (rejecting invalid ones rather than silently accepting them), indexes
//   the valid ones by id, and answers the host's lookup/grants/RPC-naming/
//   cobra-mounting queries.
// Inputs: the process-global snapshot from pkg/plugin.Builtins(), taken
//   once per Load() call.
// Outputs: an indexed BuiltinRegistry; RPCMethodName's
//   "plugin.<id>.<cmd>" strings (§D-21 mount pattern — this ticket defines
//   the naming only, D/S-06.T3 wires the actual JSON-RPC dispatch);
//   NewCobraCommand's real, non-stub *cobra.Command descriptors (RunE
//   dispatches into the registration's real BuiltinHandlers.RunCommand —
//   D/S-06.T1 mounts these under the root command).
// Constraints: import direction is internal/plugins -> pkg/plugin, NEVER
//   the reverse (Art.10.2, enforced by internal/build/arch_test.go); this
//   package is internal/, so it is free to construct raw fmt.Errorf values
//   (the boundary lint restricts pkg/ and cmd/ only) — this file uses the
//   pkg/cascade taxonomy anyway, since Load's rejection surfaces to
//   daemon/embedded-runtime boot callers who benefit from a typed Kind. No
//   bare time.Now (forbidigo) — Clock is injected but unused in W1,
//   structurally required per 02-TARGET-STRUCTURE.md §v1.1's amendments.
// SPORT: internal/plugins builtin-registry (ADD) — P1-E03-W1-S05-T7.

// Clock abstracts time.Now so BuiltinRegistry never reads the wall clock
// directly (02-TARGET-STRUCTURE.md §v1.1). Duck-typed to internal/runtime's
// and internal/testkit's Clock interfaces (both declare only
// Now() time.Time) so this package need not import either — the same
// pattern internal/testkit/clock.go documents. Unused in W1: no ticket
// task performs a temporal operation yet, but a later ticket that adds one
// (e.g. registration timestamps, TTL) can do so without breaking
// BuiltinRegistry's field shape.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// BuiltinRegistry is the host-side index of every compile-time-registered
// builtin plugin. The zero value is ready to use; call Load before any
// other method.
type BuiltinRegistry struct {
	// Clock is the injected time source. See the Clock doc comment: unused
	// in W1, structurally required for later tickets. A nil Clock is valid
	// — nothing in this ticket calls it.
	Clock Clock

	mu      sync.RWMutex
	entries map[string]plugin.BuiltinRegistration
}

// Load reads every compile-time-registered builtin plugin from
// pkg/plugin.Builtins(), validates each manifest with plugin.Validate, and
// indexes the valid ones by manifest id. An invalid manifest is REJECTED —
// excluded from the registry, never silently accepted — while every other,
// valid manifest in the same Load call is still indexed: one malformed
// plugin must not take the rest of the fleet down. Load always returns a
// non-nil error when at least one manifest was rejected, so the caller
// (daemon/embedded-runtime boot) can log and decide whether to proceed;
// the returned error's Kind is cascade.KindInvalidInput.
//
// Load may be called more than once; each call replaces the previously
// indexed set (idempotent from the caller's perspective — matching
// pkg/plugin.Builtins' own snapshot-once semantics, since repeated calls
// observe the SAME frozen snapshot).
func (r *BuiltinRegistry) Load() error {
	regs := plugin.Builtins()

	entries := make(map[string]plugin.BuiltinRegistration, len(regs))
	var rejected []string
	for _, reg := range regs {
		if errs := plugin.Validate(reg.Manifest); len(errs) > 0 {
			rejected = append(rejected, fmt.Sprintf("%s: %s", reg.Manifest.ID, errs[0]))
			continue
		}
		entries[reg.Manifest.ID] = reg
	}

	r.mu.Lock()
	r.entries = entries
	r.mu.Unlock()

	if len(rejected) > 0 {
		return cascade.Newf(
			cascade.KindInvalidInput,
			"builtin registry: rejected manifest(s): %s",
			strings.Join(rejected, "; "),
		)
	}
	return nil
}

// Get returns the builtin registration for id, if Load found and accepted
// it. ok is false when id was never registered, was rejected by Load, or
// Load has not been called yet.
func (r *BuiltinRegistry) Get(id string) (plugin.BuiltinRegistration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.entries[id]
	return reg, ok
}

// List returns every loaded builtin registration, sorted by manifest id.
// Sorting (rather than Go's undefined map-iteration order) makes the
// returned order deterministic across calls and across -shuffle=on test
// runs (Art.11 — a map-iteration-order flake is never acceptable).
func (r *BuiltinRegistry) List() []plugin.BuiltinRegistration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]plugin.BuiltinRegistration, 0, len(r.entries))
	for _, reg := range r.entries {
		out = append(out, reg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

// Grants returns the grants slice conferred on id at registration time, or
// nil if id was never loaded (never registered, rejected, or Load has not
// run).
func (r *BuiltinRegistry) Grants(id string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.entries[id]
	if !ok {
		return nil
	}
	return reg.Grants
}

// RPCMethodName returns the JSON-RPC method name a plugin command mounts
// to, following the "plugin.<pluginID>.<commandName>" pattern
// (10-ROUND2-DELTAS.md §D-21). It is a pure string-formatting helper — T7
// defines the naming convention only; D/S-06.T3 wires the actual JSON-RPC
// dispatch that consumes it. RPCMethodName performs no lookup against the
// loaded registry, so it is safe to call before Load and for a pluginID/
// commandName pair that was never registered.
func (r *BuiltinRegistry) RPCMethodName(pluginID, commandName string) string {
	return fmt.Sprintf("plugin.%s.%s", pluginID, commandName)
}

// NewCobraCommand builds a real, mountable *cobra.Command from one of a
// loaded plugin's CommandSpec entries: Use is the command's name, Short is
// its description, and RunE dispatches straight into the registration's
// own BuiltinHandlers.RunCommand — proving cobra-compatible descriptor
// structure with a genuine (non-stub) Run path, not a placeholder. ok is
// false when pluginID is not loaded or has no command named commandName.
func (r *BuiltinRegistry) NewCobraCommand(pluginID, commandName string) (*cobra.Command, bool) {
	reg, ok := r.Get(pluginID)
	if !ok {
		return nil, false
	}
	for _, cmd := range reg.Manifest.Provides.Commands {
		if cmd.Name != commandName {
			continue
		}
		handlers := reg.Handlers
		name := cmd.Name
		return &cobra.Command{
			Use:   cmd.Name,
			Short: cmd.Description,
			RunE: func(c *cobra.Command, args []string) error {
				return handlers.RunCommand(c.Context(), name, args)
			},
		}, true
	}
	return nil, false
}
