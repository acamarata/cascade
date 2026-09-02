package runtime

// Purpose: EffectiveConfig — the six-family guarded view of a resolved
//
//	config used by both the hot-reload engine's loosening gate and
//	baseline.go's boot-time divergence hash — and CompareSecurity, the
//	pure tightening-only classifier the ticket contract requires
//	(§D-26/§D-27). Split out of hotreload.go per R-14.117/Art.10.3.
//
// Inputs: two EffectiveConfig values (current, the running snapshot;
//
//	proposed, freshly parsed from an edited config.toml).
//
// Outputs: []LooseningPath — empty iff proposed is no looser than current
//
//	across every guarded family; each element names the family, the
//	dotted key, both values, and why it counts as looser.
//
// Constraints: pure function, no I/O, no clock, no global state — every
//
//	ordering decision that has no ratified total order in the P1 planning
//	corpus (autonomy_profile, trust_tier, secrets.keychain_backend
//	identity, elevation.helper_pubkey identity) is treated conservatively:
//	ANY change to that field is reported as a LooseningPath. This matches
//	§D-27's W1 doctrine ("deny-by-default needs no policy engine") —
//	under-specified is deny, never silently accept. Where the corpus DOES
//	define an order (elevation.allow_remote: false is strictly tighter
//	than true; sync domain class: local-only < synced < server-primary,
//	00-VISION.md §sync), that order is used instead of the blanket rule.
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

// EffectiveConfig is the resolved view of the six 11-ROUND3-DELTAS.md
// §D-26 guarded families, extracted from a *Config's typed sections
// (Elevation) and its Extra map (everything else — this package types
// only [runtime]/[elevation]/[logging]; policy/secrets/sync/nodes/
// conductor are read here directly from Extra, exactly as Art.1 intends:
// this ticket does not retroactively "own" those sections, it only reads
// the specific guarded keys the loosening gate needs).
type EffectiveConfig struct {
	Policy    PolicySection
	Secrets   SecretsSection
	Sync      SyncSection
	Nodes     NodesSection
	Conductor ConductorSection
	Elevation elevationSection
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
	// KeychainBackend is cold per 08 §3's [secrets] "mixed" row; treated
	// as any-change-is-loosening for the same conservative-default reason
	// as AutonomyProfile (backend identity has no ratified security
	// ordering either).
	KeychainBackend string
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
	// TrustTier has a ratified enum (controller|worker-trusted|
	// paired-device, 06-FORGE-SPEC §5 rule 22 / 10-ROUND2-DELTAS §D-11)
	// but no ratified ordering of which tier is "looser" as a config
	// default to expand toward — CompareSecurity treats any change as
	// loosening, conservatively, pending Q/S-36.T1's fuller semantics.
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
	return paths
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

// compareBool implements true-is-looser-than-false, the ratified
// direction for elevation.allow_remote (08 §[elevation]: "allow_remote =
// false" is the safe default) and the reasonable reading of the
// conductor external-routing/spill toggles (enabling either expands what
// leaves the trust boundary).
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
	return EffectiveConfig{
		Policy:  PolicySection{AutonomyProfile: stringAt(cfg.Extra, "policy", "autonomy_profile")},
		Secrets: SecretsSection{KeychainBackend: stringAt(cfg.Extra, "secrets", "keychain_backend")},
		Sync:    SyncSection{Classes: stringMapAt(cfg.Extra, "sync")},
		Nodes:   NodesSection{TrustTier: stringAt(cfg.Extra, "nodes", "trust_tier")},
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
