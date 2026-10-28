// Package plugins (intent_dispatch.go): Purpose: the host-side caller of
// pkg/plugin.BuiltinHandlers.DispatchIntent. IntentDispatcher routes one
// named intent to exactly one enabled builtin plugin, or refuses.
//
// Inputs: a loaded *BuiltinRegistry, an intent name and the opaque input
// bytes the target plugin's DispatchIntent decodes itself.
// Outputs: that plugin's DispatchIntent bytes, unchanged; or KindNotFound
// (nothing declares the intent), KindConflict (more than one enabled plugin
// declares it, ids listed) or the resolver's own KindInvalidInput for a
// blank name. A refusal calls no plugin at all.
// Constraints: matching reuses the canonical intent resolver's installed-
// first step (internal/plugins/resolver) with no registry index, so the
// typed ambiguity is the resolver's, never a second matcher. "Enabled" is
// pkg/plugin/register.go's contract: loaded by Load and holding a non-empty
// Grants set. There is no fallback: exactly one match or a refusal. Explicit
// invocation only: `cascade plugin add <intent-ref>` and the plugin.intent
// RPC are the callers; no chat, bridge or conversation path holds one.
// SPORT: internal/plugins intent-dispatch (ADD) — P1-PLG-07.
package plugins

import (
	"context"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// IntentDispatcher routes a named intent to the one enabled builtin plugin
// that declares it. Construct with NewIntentDispatcher.
type IntentDispatcher struct {
	registry *BuiltinRegistry
	resolver plugin.Resolver
}

// NewIntentDispatcher returns a dispatcher over r, which the caller has
// already loaded. A nil r dispatches nothing: every intent is KindNotFound.
func NewIntentDispatcher(r *BuiltinRegistry) *IntentDispatcher {
	return &IntentDispatcher{registry: r, resolver: resolver.NewIntentResolver()}
}

// Dispatch resolves intentName against the enabled registrations and calls
// the single match's DispatchIntent with input. Zero matches is
// KindNotFound; more than one is KindConflict naming every matching plugin
// id. The plugin's own result and error are returned unchanged.
func (d *IntentDispatcher) Dispatch(ctx context.Context, intentName string, input []byte) ([]byte, error) {
	installed, handlers := d.enabledSet()
	cands, err := d.resolver.Resolve(ctx, installed, nil, intentName)
	if err != nil {
		return nil, mapResolveError(intentName, err)
	}
	if len(cands) != 1 {
		return nil, cascade.Newf(cascade.KindInternal,
			"plugin intent %q: resolver returned %d candidates without an ambiguity error", intentName, len(cands))
	}
	h, ok := handlers[cands[0].PluginID]
	if !ok || cands[0].Source != plugin.CandidateSourceInstalled {
		return nil, cascade.Newf(cascade.KindInternal,
			"plugin intent %q: resolver named %q, which is not an enabled builtin", intentName, cands[0].PluginID)
	}
	return h.DispatchIntent(ctx, declaredIntentName(cands[0].Manifest, intentName), input)
}

// declaredIntentName returns the name m itself declares for the intent the
// resolver matched (trimmed, case-insensitive), so the plugin receives its
// own spelling rather than the caller's.
func declaredIntentName(m plugin.Manifest, intentName string) string {
	needle := strings.ToLower(strings.TrimSpace(intentName))
	for _, is := range m.Provides.Intents {
		if strings.ToLower(strings.TrimSpace(is.Name)) == needle {
			return is.Name
		}
	}
	return intentName
}

// enabledSet snapshots the registrations that may serve an intent: loaded
// and granted. The handler map is keyed by plugin id so the dispatch goes
// to the exact registration the resolver named.
func (d *IntentDispatcher) enabledSet() (plugin.ManifestSet, map[string]plugin.BuiltinHandlers) {
	handlers := map[string]plugin.BuiltinHandlers{}
	if d.registry == nil {
		return nil, handlers
	}
	var set plugin.ManifestSet
	for _, reg := range d.registry.List() {
		if len(reg.Grants) == 0 || reg.Handlers == nil {
			continue
		}
		set = append(set, plugin.InstalledPlugin{Manifest: reg.Manifest, Enabled: true})
		handlers[reg.Manifest.ID] = reg.Handlers
	}
	return set, handlers
}

// mapResolveError turns the resolver's installed-first outcomes into the
// dispatcher's contract. With no index, "nothing installed declares it"
// surfaces as ErrUnverifiedIndex; for an intent dispatch that is absence.
func mapResolveError(intentName string, err error) error {
	var amb *plugin.AmbiguousIntent
	switch {
	case errors.As(err, &amb):
		ids := make([]string, 0, len(amb.Candidates))
		for _, c := range amb.Candidates {
			ids = append(ids, c.PluginID)
		}
		return cascade.Newf(cascade.KindConflict,
			"plugin intent %q is declared by more than one enabled plugin (%s); nothing was dispatched",
			intentName, strings.Join(ids, ", "))
	case err == plugin.ErrUnverifiedIndex || err == plugin.ErrIntentNotFound:
		// Sentinel identity on purpose: errors.Is on a cascade error
		// compares Kind only, and these two share their Kind with others.
		return cascade.Newf(cascade.KindNotFound,
			"plugin intent %q: no enabled plugin declares it; nothing was dispatched", intentName)
	default:
		return err
	}
}
