// Purpose: the `[registry]` table parser behind Load (config.go):
// 08-INIT-CONFIG-SPEC.md §3's url/cache_dir/cache_ttl/pubkey_path fields
// (P1-E24-W5-S50-T2, R-14.75). Split out as its own sibling file per
// R-14.117's established remedy — config_widget.go, config_plugins.go and
// config_economics.go do the same, rather than growing config.go past its
// 300-line cap.
//
// FILES_SCOPE DEVIATION (recorded per LANE-RULES §1, same reasoning
// config_widget.go's own header already states for itself, and the T0
// decision this file implements — s50t2-t0-decisions.txt D1). The ticket
// text this file answers names no files_scope entry for it at all (the
// draft this file replaces read CASCADE_REGISTRY__* directly via
// os.Getenv, bypassing internal/runtime entirely — the adversarial
// review's BLOCK 1). This file follows the shipped config_widget.go/
// config_plugins.go convention instead of inventing a new subpackage or
// leaving the bypass in place.
//
// NO IN-BINARY DEFAULT PUBLIC KEY (recorded deviation, T0-decided
// 2026-09-21 per s50t2-t0-decisions.txt D1). 08-INIT-CONFIG-SPEC.md §3
// says "[registry] ... in-binary default pubkey" (R-14.75), but the only
// Ed25519 keypair anywhere in this tree today is X/S-50.T1's own
// self-authored TEST fixture (see pkg/plugin/testdata/registry/README.md
// and that ticket's journal, P1-E24-W5-S50-T1.md: "the fixture is also
// NOT produced by the real minisign tool"). Promoting a test fixture to
// the production trust root would make every real registry index
// unverifiable while LOOKING verified — the one failure mode a signature
// scheme exists to prevent. PubkeyPath resolving to "" is therefore a
// real, disclosed, and correctly fail-closed state (never a bug in this
// parser): the caller (cmd/cascade/plugin_search.go's
// buildPluginRegistryClient) treats an absent or unreadable key exactly
// like an absent registry — builtin catalog only, a logged warning, and
// internal/doctor's registry_pubkey finding names the gap. Recorded here
// as the owner action item: a real registry signing keypair must be
// generated and its public half shipped (or configured) before this
// default can ever be filled in — never asked about, per GCI decision
// authority.
//
// Inputs: the decoded generic config tree (env overrides already applied
// — CASCADE_REGISTRY__URL / CASCADE_REGISTRY__CACHE_DIR /
// CASCADE_REGISTRY__CACHE_TTL / CASCADE_REGISTRY__PUBKEY_PATH resolve
// through config_env.go's generic collectEnvOverrides machinery before
// this parser ever runs — this file never reads os.Getenv itself).
// Outputs: a registrySection, or a typed *ConfigError for an unknown
// top-level key or a type mismatch.
// Constraints: cache_ttl defaults to 15 minutes when unset (a
// conservative, documented default — the spec table names no numeric
// default). cache_dir defaults are resolved by the CALLER against the
// live PathProvider (runtimeSection's own established rule: Home/DataDir
// are never read from config.toml — see config.go), never invented here.
// SPORT: runtime/config-registry (ADD, P1-E24-W5-S50-T2).

package runtime

import "time"

// registrySection is the `[registry]` table's typed view (R-14.75).
type registrySection struct {
	// URL is the registry root. Empty means "no registry configured" —
	// the caller never even constructs a client (R-14.93 fail-closed).
	URL string
	// CacheDir is the on-disk cache location. Empty means "use the
	// caller's PathProvider-derived default" (see this file's header).
	CacheDir string
	// CacheTTL is how long a cached, verified index is served without a
	// re-fetch.
	CacheTTL time.Duration
	// PubkeyPath is the path to the registry's Ed25519 public key file.
	// Empty means no key is configured — see this file's header on why
	// that is the shipped default in this tree today.
	PubkeyPath string
}

// defaultRegistryCacheTTL is 08 §3's shipped default when cache_ttl is
// unset.
const defaultRegistryCacheTTL = 15 * time.Minute

// registryTopKeys is the closed set of keys a `[registry]` table
// recognises.
var registryTopKeys = map[string]bool{
	"url": true, "cache_dir": true, "cache_ttl": true, "pubkey_path": true,
}

// parseRegistrySection type-checks tree's `[registry]` table. An absent
// table parses to the zero URL (unconfigured) with the default TTL, not
// an error — matching every other optional section in this package.
func parseRegistrySection(tree map[string]interface{}) (registrySection, error) {
	sec := registrySection{CacheTTL: defaultRegistryCacheTTL}
	raw, ok := tree["registry"].(map[string]interface{})
	if !ok {
		return sec, nil
	}
	for k := range raw {
		if !registryTopKeys[k] {
			return registrySection{}, &ConfigError{Field: "registry." + k, Reason: "unrecognised key in [registry]"}
		}
	}
	var err error
	if sec.URL, err = registryStringField(raw, "url"); err != nil {
		return registrySection{}, err
	}
	if sec.CacheDir, err = registryStringField(raw, "cache_dir"); err != nil {
		return registrySection{}, err
	}
	if sec.PubkeyPath, err = registryStringField(raw, "pubkey_path"); err != nil {
		return registrySection{}, err
	}
	if sec.CacheTTL, err = registryDurationField(raw, "cache_ttl", defaultRegistryCacheTTL); err != nil {
		return registrySection{}, err
	}
	return sec, nil
}

// registryStringField reads an optional string leaf, or a typed
// *ConfigError when the key is present but not a string.
func registryStringField(raw map[string]interface{}, key string) (string, error) {
	v, present := raw[key]
	if !present {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", &ConfigError{Field: "registry." + key, Reason: "must be a string"}
	}
	return s, nil
}

// addRegistryEffectiveEntries writes sec's four leaves into values, dotted
// -path style, for Config.EffectiveEntries (config.go) -- factored out
// purely to keep that file under Art.10.3's 300-line cap (config_sections.
// go's own header records the same prior incident, R-14.204).
func addRegistryEffectiveEntries(sec registrySection, values map[string]interface{}) {
	values["registry.url"] = sec.URL
	values["registry.cache_dir"] = sec.CacheDir
	values["registry.cache_ttl"] = sec.CacheTTL.String()
	values["registry.pubkey_path"] = sec.PubkeyPath
}

// registryDurationField reads an optional duration-string leaf (e.g.
// "15m"), defaulting to fallback when unset.
func registryDurationField(raw map[string]interface{}, key string, fallback time.Duration) (time.Duration, error) {
	v, present := raw[key]
	if !present {
		return fallback, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, &ConfigError{Field: "registry." + key, Reason: "must be a duration string (e.g. \"15m\")"}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, &ConfigError{Field: "registry." + key, Reason: "not a valid duration: " + err.Error()}
	}
	return d, nil
}
