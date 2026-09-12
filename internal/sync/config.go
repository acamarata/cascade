// Purpose: the `[sync]` config section (08-INIT-CONFIG-SPEC.md §3):
//   per-domain sync class overrides, hot-reloadable, tightening-only
//   (C/S-05.T8 §D-26). CONTRADICTION (files_scope vs tree): the contract
//   describes this landing "through the C/S-04.T1 config frame"
//   (internal/runtime/config.go's parseConfigSections dispatch); that
//   file is NOT in this ticket's files_scope (only internal/sync/config.go
//   is), so this file is a self-contained parser + tightening-only
//   validator internal/runtime's own config owner wires into
//   parseConfigSections in a future change — exactly the shape
//   internal/runtime/config_sections.go's existing sections
//   (elevationSection, loggingSection, retrievalSection) already take,
//   so the wiring is a single parseXSection call once landed.
// Inputs: the decoded config tree (map[string]interface{}, matching
//   internal/runtime's own section-parser signature) plus the PREVIOUS
//   resolved Config, for the tightening-only check.
// Outputs: a Config, or a *cascade.Error.
// Constraints: TIGHTENING-ONLY (D-26): a hot reload may only NARROW a
//   domain's sync class (synced -> local-only) or leave it unchanged,
//   never widen it (local-only -> synced) without a cold restart —
//   ParseSection enforces this against the previous Config it is given.
// SPORT: internal.sync.config/ADDED (P1-E17-W4-S38-T1).

package sync

import "github.com/acamarata/cascade/pkg/cascade"

// Config is the [sync] section's resolved shape: per-domain-subkind class
// overrides, keyed the same way domains.go's registryKey is (as a plain
// string "domain/subkind" for config-file legibility).
type Config struct {
	// Overrides narrows (never widens) a registered DomainClass's Class.
	// A key not present here uses domains.go's compiled-in default.
	Overrides map[string]Class
}

// classRank orders Class from most to least permissive, so "narrower"
// has an unambiguous meaning for the tightening-only check.
var classRank = map[Class]int{
	ClassServerPrimary: 3,
	ClassSyncedAppend:  2,
	ClassSynced:        2,
	ClassLocalOnly:     1,
}

// isNarrowing reports whether next is no more permissive than prev.
func isNarrowing(prev, next Class) bool {
	return classRank[next] <= classRank[prev]
}

// ParseSection decodes tree's `sync` table into a Config. previous is nil
// for a cold start (no tightening check applies); on a hot reload,
// previous is the currently-active Config and every overridden key must
// narrow or hold, never widen — a widening key is refused as
// KindPolicyDenied (a config change trying to loosen a security posture
// without a restart) rather than silently applied or silently dropped.
func ParseSection(tree map[string]interface{}, previous *Config) (Config, error) {
	raw, ok := tree["sync"]
	if !ok {
		return Config{}, nil
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return Config{}, cascade.New(cascade.KindInvalidInput, "sync: [sync] section must be a table")
	}
	overrides := make(map[string]Class, len(section))
	for key, v := range section {
		str, ok := v.(string)
		if !ok {
			return Config{}, cascade.Newf(cascade.KindInvalidInput, "sync: [sync].%s must be a string sync class", key)
		}
		class := Class(str)
		if !class.Valid() {
			return Config{}, cascade.Newf(cascade.KindInvalidInput, "sync: [sync].%s names an unknown sync class %q", key, str)
		}
		overrides[key] = class
	}
	if previous != nil {
		for key, next := range overrides {
			if prev, existed := previous.Overrides[key]; existed && !isNarrowing(prev, next) {
				return Config{}, cascade.Newf(cascade.KindPolicyDenied, "sync: hot reload may only narrow [sync].%s (was %q, refusing widen to %q); restart to widen", key, prev, next)
			}
		}
	}
	return Config{Overrides: overrides}, nil
}

// Resolve applies cfg's overrides on top of domains.go's compiled-in
// DomainClass for domain+subkind, returning the effective Class.
func (cfg Config) Resolve(key string, compiled Class) Class {
	if override, ok := cfg.Overrides[key]; ok {
		return override
	}
	return compiled
}
