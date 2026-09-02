package runtime

// Purpose: ComputeSectionsHash and MostRestrictiveDefaults — split out of
//
//	baseline.go per R-14.117/Art.10.3 (300-line file cap).
//
// Inputs: an EffectiveConfig.
// Outputs: a canonical SHA-256 hex digest; the fail-closed floor config.
// Constraints: deterministic, no I/O, no clock.
// SPORT: runtime/boot-baseline-check (ADD, placeholder per T-8 sport_updates).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ComputeSectionsHash computes the canonical SHA-256 over cfg's six
// guarded sections: JSON-marshal each with keys sorted deterministically
// (encoding/json already sorts map keys; struct field order is fixed by
// Go's own declaration order, which is stable across builds), concatenate
// in baselineGuardedSections' fixed order, hash.
func ComputeSectionsHash(cfg EffectiveConfig) string {
	h := sha256.New()
	sections := map[string]interface{}{
		"policy":    cfg.Policy,
		"secrets":   cfg.Secrets,
		"sync":      sortedSyncClasses(cfg.Sync),
		"nodes":     cfg.Nodes,
		"conductor": cfg.Conductor,
		"elevation": cfg.Elevation,
	}
	for _, name := range baselineGuardedSections {
		b, _ := json.Marshal(sections[name]) // encoding/json errors only on unsupported types (chan/func); none appear in these structs
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sortedSyncClasses renders SyncSection.Classes as a deterministically
// ordered slice of pairs, since JSON-marshaling a Go map is already
// key-sorted but the []struct form keeps this function's intent explicit
// and independent of that encoding/json implementation detail.
func sortedSyncClasses(s SyncSection) []struct{ Domain, Class string } {
	domains := make([]string, 0, len(s.Classes))
	for d := range s.Classes {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	out := make([]struct{ Domain, Class string }, 0, len(domains))
	for _, d := range domains {
		out = append(out, struct{ Domain, Class string }{Domain: d, Class: s.Classes[d]})
	}
	return out
}

// MostRestrictiveDefaults returns the most-restrictive shipped defaults
// for all six guarded sections — the fail-closed floor applied on a
// missing or divergent baseline (§D-39). Every field here is the tightest
// value 08-INIT-CONFIG-SPEC.md's design documents for its family:
// elevation.allow_remote=false is 08's own documented default; every
// other field has no documented default (Art.1/R-14.107 precedent: never
// invent one), so its zero value is used, which for every guarded field
// in this ticket's model (autonomy_profile="" / keychain_backend="" /
// trust_tier="" / external_routing_enabled=false / spill_enabled=false)
// is already its most restrictive shape — an empty/false guarded field
// reads as "not configured", never as "loosest possible", under every
// comparison function in hotreload_security.go.
func MostRestrictiveDefaults() EffectiveConfig {
	return EffectiveConfig{
		Sync:      SyncSection{Classes: map[string]string{}},
		Elevation: elevationSection{AllowRemote: false, HelperPubkey: ""},
	}
}
