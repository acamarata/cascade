package runtime

// Purpose: EffectiveConfig, the guarded view of a resolved config used by
//
//	the hot-reload loosening gate and the boot-time baseline hash, and
//	CompareSecurity, the pure tightening-only classifier (§D-26/§D-27).
//
// Inputs: two EffectiveConfig values (current, the running snapshot;
//
//	proposed, freshly parsed from an edited config.toml).
//
// Outputs: []LooseningPath, empty iff proposed is no looser than current in
//
//	any guarded field or family; each names the family, the dotted key, both
//	values, and why it counts as looser.
//
// Constraints: pure, no I/O, no clock. A field with no ratified order
//
//	(autonomy_profile, trust_tier, keychain_backend, helper_pubkey) counts
//	ANY change as loosening (§D-27 W1: under-specified is deny). Where the
//	corpus defines an order (allow_remote false < true; sync local-only <
//	synced < server-primary, 00-VISION §sync) that order is used. Registered
//	guarded families (guarded_families.go) compare through their own rules.
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"encoding/json"
	"time"
)

// EffectiveConfig is the resolved view of the §D-26 guarded sections: the
// typed Elevation section, the guarded keys read from Extra, and the
// registered families' values.
type EffectiveConfig struct {
	Policy    PolicySection
	Secrets   SecretsSection
	Sync      SyncSection
	Nodes     NodesSection
	Conductor ConductorSection
	Elevation elevationSection
	// Families holds each registered guarded family's canonical value
	// (guarded_families.go); a family at its shipped default is absent.
	Families map[string]json.RawMessage `json:",omitempty"`
}

// PolicySection is the guarded slice of [policy] this ticket reads.
type PolicySection struct {
	// AutonomyProfile has no ratified total order in the P1 corpus as of
	// this ticket (04-PEWS-PLAN-W1-W3.md defers the enum to Q/S-18.T1);
	// CompareSecurity treats any change to it as a LooseningPath.
	AutonomyProfile string
}

// SecretsSection is the guarded slice of [secrets] this ticket reads.
type SecretsSection struct {
	// KeychainBackend has no ratified security ordering: any change loosens.
	KeychainBackend string
	// EntropyFloor and ConfidenceThreshold are the detector's thresholds.
	// nil is the shipped default (an explicit default is normalised to nil),
	// so an absent key, a default key and an upgraded v1 snapshot all hash
	// and compare alike. A raise is a loosening (hotreload_secrets.go).
	EntropyFloor        *float64 `json:",omitempty"`
	ConfidenceThreshold *float64 `json:",omitempty"`
	// ClipboardTTL is secrets.clipboard_ttl, nil at the shipped default; a
	// raise is a loosening. SecretsClipboardTTL is its only reader.
	ClipboardTTL *time.Duration `json:",omitempty"`
}

// SyncSection is the guarded per-domain sync-class map. Classes maps a
// sync domain name to its class string; a domain absent from the map is
// treated as the most restrictive class ("local-only") for comparison
// purposes, so a newly-appeared domain counts as loosening unless it is
// itself local-only.
type SyncSection struct {
	Classes map[string]string
}

// syncClassRank orders sync domain classes from most to least restrictive
// per 00-VISION.md §sync ("local-only / synced / server-primary").
var syncClassRank = map[string]int{
	"local-only":     0,
	"synced":         1,
	"server-primary": 2,
}

// NodesSection is the guarded slice of [nodes] this ticket reads.
type NodesSection struct {
	// TrustTier's enum (06-FORGE-SPEC §5 rule 22) has no ratified order of
	// looseness, so any change is treated as loosening.
	TrustTier string
}

// ConductorSection is the guarded slice of [conductor] this ticket reads.
type ConductorSection struct {
	ExternalRoutingEnabled bool
	SpillEnabled           bool
}

// LooseningPath names one guarded field whose proposed value is no
// tighter (and is strictly looser, or of undetermined direction treated
// conservatively as looser) than its current value.
type LooseningPath struct {
	Family   string
	Key      string
	Current  interface{}
	Proposed interface{}
	Reason   string
}

// CompareSecurity compares current and proposed across the six guarded
// families and returns every field that is not clearly no-looser. An
// empty result means proposed is safe to hot-apply from a
// tightening-only standpoint (the caller — HotReloader.Reload — still
// separately hard-rejects any [elevation] change in either direction;
// see hotreload.go).
func CompareSecurity(current, proposed EffectiveConfig) []LooseningPath {
	var paths []LooseningPath
	paths = append(paths, compareAnyChange("policy", "policy.autonomy_profile",
		current.Policy.AutonomyProfile, proposed.Policy.AutonomyProfile)...)
	paths = append(paths, compareAnyChange("secrets", "secrets.keychain_backend",
		current.Secrets.KeychainBackend, proposed.Secrets.KeychainBackend)...)
	paths = append(paths, compareSecretsThresholds(current.Secrets, proposed.Secrets)...)
	paths = append(paths, compareClipboardTTL(current.Secrets.ClipboardTTL, proposed.Secrets.ClipboardTTL)...)
	paths = append(paths, compareSyncClasses(current.Sync, proposed.Sync)...)
	paths = append(paths, compareAnyChange("nodes", "nodes.trust_tier",
		current.Nodes.TrustTier, proposed.Nodes.TrustTier)...)
	paths = append(paths, compareBool("conductor", "conductor.external_routing_enabled",
		current.Conductor.ExternalRoutingEnabled, proposed.Conductor.ExternalRoutingEnabled)...)
	paths = append(paths, compareBool("conductor", "conductor.spill_enabled",
		current.Conductor.SpillEnabled, proposed.Conductor.SpillEnabled)...)
	paths = append(paths, compareBool("elevation", "elevation.allow_remote",
		current.Elevation.AllowRemote, proposed.Elevation.AllowRemote)...)
	paths = append(paths, compareAnyChange("elevation", "elevation.helper_pubkey",
		current.Elevation.HelperPubkey, proposed.Elevation.HelperPubkey)...)
	return append(paths, compareFamilies(current.Families, proposed.Families)...)
}

// compareAnyChange implements the conservative "no ratified order: any
// change counts as loosening" rule shared by AutonomyProfile,
// KeychainBackend, TrustTier, and HelperPubkey.
func compareAnyChange(family, key, current, proposed string) []LooseningPath {
	if current == proposed {
		return nil
	}
	return []LooseningPath{{
		Family: family, Key: key, Current: current, Proposed: proposed,
		Reason: "no ratified security ordering for this field; any change is treated as loosening (W1 conservative default)",
	}}
}

// compareBool implements true-is-looser-than-false: elevation.allow_remote
// (08: false is the safe default) and the conductor external-routing/spill
// toggles (enabling either expands what leaves the trust boundary).
func compareBool(family, key string, current, proposed bool) []LooseningPath {
	if !proposed || proposed == current {
		return nil
	}
	return []LooseningPath{{
		Family: family, Key: key, Current: current, Proposed: proposed,
		Reason: "false -> true expands permissions/egress",
	}}
}

// compareSyncClasses compares every domain present in either current or
// proposed by syncClassRank; an unranked class string is treated as
// maximally loose (fail-closed on an unrecognised class name).
func compareSyncClasses(current, proposed SyncSection) []LooseningPath {
	var paths []LooseningPath
	domains := map[string]bool{}
	for d := range current.Classes {
		domains[d] = true
	}
	for d := range proposed.Classes {
		domains[d] = true
	}
	for d := range domains {
		curClass := current.Classes[d]
		if curClass == "" {
			curClass = "local-only"
		}
		propClass := proposed.Classes[d]
		if propClass == "" {
			propClass = "local-only"
		}
		if curClass == propClass {
			continue
		}
		curRank, curKnown := syncClassRank[curClass]
		propRank, propKnown := syncClassRank[propClass]
		if !curKnown || !propKnown || propRank > curRank {
			paths = append(paths, LooseningPath{
				Family: "sync", Key: "sync." + d, Current: curClass, Proposed: propClass,
				Reason: "sync domain class moved to a less restrictive tier (or is unrecognised, fail-closed)",
			})
		}
	}
	return paths
}

// extractEffectiveConfig reads the six guarded families out of cfg: the
// typed Elevation section directly, and policy/secrets/sync/nodes/
// conductor from cfg.Extra (this package's only untyped-section read —
// deliberately narrow, see this file's package doc).
func extractEffectiveConfig(cfg *Config) EffectiveConfig {
	if cfg == nil {
		return EffectiveConfig{Sync: SyncSection{Classes: map[string]string{}}}
	}
	secrets := secretsSectionAt(cfg.Extra)
	secrets.ClipboardTTL = clipboardTTLAt(cfg.Extra)
	return EffectiveConfig{
		Policy:   PolicySection{AutonomyProfile: stringAt(cfg.Extra, "policy", "autonomy_profile")},
		Secrets:  secrets,
		Families: extractFamilies(cfg.Extra),
		Sync:     SyncSection{Classes: stringMapAt(cfg.Extra, "sync")},
		Nodes:    NodesSection{TrustTier: stringAt(cfg.Extra, "nodes", "trust_tier")},
		Conductor: ConductorSection{
			ExternalRoutingEnabled: boolAt(cfg.Extra, "conductor", "external_routing_enabled"),
			SpillEnabled:           boolAt(cfg.Extra, "conductor", "spill_enabled"),
		},
		Elevation: cfg.Elevation,
	}
}

func sectionAt(extra map[string]interface{}, section string) map[string]interface{} {
	if extra == nil {
		return nil
	}
	m, _ := extra[section].(map[string]interface{})
	return m
}

func stringAt(extra map[string]interface{}, section, key string) string {
	m := sectionAt(extra, section)
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func boolAt(extra map[string]interface{}, section, key string) bool {
	m := sectionAt(extra, section)
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

// stringMapAt reads section's top-level keys as a string->string map,
// skipping any non-string-valued key (nested tables under [sync], if any
// ever appear, are not domain-class entries and are ignored rather than
// mis-parsed).
func stringMapAt(extra map[string]interface{}, section string) map[string]string {
	m := sectionAt(extra, section)
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// clipboardTTLAt reads secrets.clipboard_ttl, a duration string ("45s") or
// whole seconds: nil when absent or the default, invalidClipboardTTL for
// anything that is not a positive duration.
func clipboardTTLAt(extra map[string]interface{}) *time.Duration {
	raw, ok := sectionAt(extra, "secrets")["clipboard_ttl"]
	if !ok {
		return nil
	}
	d := invalidClipboardTTL
	if v, isStr := raw.(string); isStr {
		if p, err := time.ParseDuration(v); err == nil && p > 0 {
			d = p
		}
	} else if v, isInt := raw.(int64); isInt && v > 0 && v <= int64(invalidClipboardTTL/time.Second) {
		d = time.Duration(v) * time.Second
	}
	if d == DefaultSecretsClipboardTTL {
		return nil
	}
	return &d
}

// applyClipboardTTL writes ttl into extra's [secrets] (nil removes the key).
func applyClipboardTTL(extra map[string]interface{}, ttl *time.Duration) {
	if sectionAt(extra, "secrets") == nil && ttl == nil {
		return
	}
	sec := cloneSection(extra, "secrets")
	delete(sec, "clipboard_ttl")
	if ttl != nil {
		sec["clipboard_ttl"] = ttl.String()
	}
	extra["secrets"] = sec
}
