package hooks

import (
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the hook registry — validates and stores HookConfig values,
//
//	refusing a bad namespace or an unrecognised action_type BEFORE a hook
//	is stored. Whether a recognised type can run is the dispatcher's
//	question (Dispatcher.Runnable), asked at load by ParseHooksSection
//	and again at every dispatch.
//
// Inputs: HookConfig values from Register's caller (composition-root
//
//	config loading, out of scope here).
//
// Outputs: the stored/derived HookConfig on success; a
//
//	cascade.KindPolicyDenied error (HookActionNotPermittedCode) for an
//	unknown action type, cascade.KindInvalidInput for a bad namespace, a
//	missing trigger or the reserved hooks-audit trigger,
//	cascade.KindConflict for a duplicate explicit ID.
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
//  1. cfg.Namespace must be a valid hook namespace and never the audit
//     namespace (namespaceProblem, config.go).
//  2. cfg.Trigger must be non-empty and must not name the package's own
//     audit EventKind (EventKindHookFire): a hook can never be configured
//     to fire on its own audit trail.
//  3. cfg.ActionType must be a recognised type (permittedActionTypes).
//     Whether it can RUN is the dispatcher's question (Runnable), asked
//     again at every dispatch.
//  4. A non-empty explicit cfg.ID must not already be registered.
//
// A rejected hook is NEVER stored. The stored copy owns its params map.
func (r *Registry) Register(cfg HookConfig) (HookConfig, error) {
	if problem := namespaceProblem(cfg.Namespace); problem != "" {
		return HookConfig{}, cascade.Newf(cascade.KindInvalidInput, "hooks: register: namespace %s", problem)
	}
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
		cfg.ID = DeriveHookID(cfg.Namespace, cfg.Trigger, cfg.ActionType, cfg.ActionParams)
	}
	cfg.ActionParams = cloneParams(cfg.ActionParams)

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

// List returns a copy of every currently-registered hook, sorted by ID.
func (r *Registry) List() []HookConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HookConfig, 0, len(r.hooks))
	for _, cfg := range r.hooks {
		cfg.ActionParams = cloneParams(cfg.ActionParams)
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MatchTriggers returns every registered hook listening on namespace whose
// Trigger equals kind exactly, sorted by ID. An empty result is not an
// error.
func (r *Registry) MatchTriggers(namespace, kind string) []HookConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []HookConfig
	for _, cfg := range r.hooks {
		if cfg.Namespace == namespace && cfg.Trigger == kind {
			out = append(out, cfg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Namespaces returns the distinct namespaces the registered hooks listen
// on, sorted. The dispatcher holds one subscription per entry.
func (r *Registry) Namespaces() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, cfg := range r.hooks {
		if !seen[cfg.Namespace] {
			seen[cfg.Namespace] = true
			out = append(out, cfg.Namespace)
		}
	}
	sort.Strings(out)
	return out
}

// cloneParams returns an independent copy of params (nil stays nil).
func cloneParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// symmetricDifference returns the sorted names in exactly one of a and b.
func symmetricDifference(a, b []string) []string {
	count := make(map[string]int)
	for _, ns := range union(a, nil) {
		count[ns]++
	}
	for _, ns := range union(b, nil) {
		count[ns]++
	}
	var out []string
	for ns, n := range count {
		if n == 1 {
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

// union returns the sorted distinct names in a and b.
func union(a, b []string) []string {
	var out []string
	for _, ns := range append(append([]string(nil), a...), b...) {
		if !contains(out, ns) {
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

// contains reports whether set holds ns.
func contains(set []string, ns string) bool {
	for _, s := range set {
		if s == ns {
			return true
		}
	}
	return false
}

// EventKindHookFire is declared here (rather than audit.go) so
// Register's reserved-trigger check above and audit.go's Publish call
// share one symbol. See events/types.go's EventKind doc: internal/events
// is deliberately open, and generic infrastructure packages mint their
// own EventKind values against it.
const EventKindHookFire events.EventKind = "hooks.fire"
