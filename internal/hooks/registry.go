package hooks

import (
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the hook registry — validates and stores HookConfig values,
//
//	refusing shell and any unrecognised action_type BEFORE a hook is
//	stored (task 2's requirement: registration-time refusal, the first of
//	this package's two defense-in-depth checks — dispatcher.go performs
//	the second, at dispatch time).
//
// Inputs: HookConfig values from Register's caller (composition-root
//
//	config loading, out of scope here).
//
// Outputs: the stored/derived HookConfig on success; a
//
//	cascade.KindPolicyDenied error (HookActionNotPermittedCode) for
//	shell/unknown action types, cascade.KindInvalidInput for a missing
//	trigger or the reserved hooks-audit trigger, cascade.KindConflict for
//	a duplicate explicit ID.
//
// Constraints: thread-safe (sync.RWMutex) — Register/Deregister/List/
//
//	MatchTriggers may all be called concurrently (-race clean, this
//	package's own concurrent-registration coverage). List and
//	MatchTriggers return results sorted by ID so iteration order is
//	deterministic under -shuffle=on (Art.11).
//
// SPORT: internal.hooks.Registry/ADDED (P1-E03-W1-S05-T1).

// Registry holds the set of currently-registered hooks. The zero value is
// not usable; construct with NewRegistry.
type Registry struct {
	mu    sync.RWMutex
	hooks map[string]HookConfig
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{hooks: make(map[string]HookConfig)}
}

// Register validates cfg and, if valid, stores it (deriving cfg.ID via
// DeriveHookID first if the caller left it empty) and returns the stored
// HookConfig. Validation, in order:
//
//  1. cfg.Trigger must be non-empty and must not name the package's own
//     audit EventKind (EventKindHookFire) — refusing that trigger up
//     front is this package's first re-entrancy guard (doc.go): a hook
//     can never be configured to fire on its own audit trail.
//  2. cfg.ActionType must be ActionTypePluginCall or ActionTypeAgentNote.
//     ActionTypeShell and any other value — including the empty string —
//     are refused with newActionNotPermittedError
//     (HookActionNotPermittedCode).
//  3. A non-empty explicit cfg.ID must not already be registered.
//
// A rejected hook is NEVER stored — Register returns before touching the
// map on any validation failure.
func (r *Registry) Register(cfg HookConfig) (HookConfig, error) {
	if cfg.Trigger == "" {
		return HookConfig{}, cascade.New(cascade.KindInvalidInput, "hooks: register: trigger must not be empty")
	}
	if cfg.Trigger == string(EventKindHookFire) {
		return HookConfig{}, cascade.Newf(
			cascade.KindInvalidInput,
			"hooks: register: trigger %q is reserved for the hooks audit trail and may not be a hook's own trigger",
			cfg.Trigger,
		)
	}
	if !permittedActionTypes[cfg.ActionType] {
		return HookConfig{}, newActionNotPermittedError(cfg.ActionType)
	}

	if cfg.ID == "" {
		cfg.ID = DeriveHookID(cfg.Trigger, cfg.ActionType, cfg.ActionParams)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.hooks[cfg.ID]; exists {
		return HookConfig{}, cascade.Newf(cascade.KindConflict, "hooks: register: id %q already registered", cfg.ID)
	}
	r.hooks[cfg.ID] = cfg
	return cfg, nil
}

// Deregister removes id from the registry. It returns a
// cascade.KindNotFound error if id is not currently registered.
func (r *Registry) Deregister(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.hooks[id]; !exists {
		return cascade.Newf(cascade.KindNotFound, "hooks: deregister: id %q not registered", id)
	}
	delete(r.hooks, id)
	return nil
}

// List returns every currently-registered hook, sorted by ID.
func (r *Registry) List() []HookConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HookConfig, 0, len(r.hooks))
	for _, cfg := range r.hooks {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MatchTriggers returns every registered hook whose Trigger equals
// trigger exactly, sorted by ID. An empty result is not an error — the
// dispatcher's handling of "no match" (silent no-op, no audit) lives in
// dispatcher.go, not here.
func (r *Registry) MatchTriggers(trigger string) []HookConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []HookConfig
	for _, cfg := range r.hooks {
		if cfg.Trigger == trigger {
			out = append(out, cfg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EventKindHookFire is declared here (rather than audit.go) so
// Register's reserved-trigger check above and audit.go's Publish call
// share one symbol. See events/types.go's EventKind doc: internal/events
// is deliberately open, and generic infrastructure packages mint their
// own EventKind values against it.
const EventKindHookFire events.EventKind = "hooks.fire"
