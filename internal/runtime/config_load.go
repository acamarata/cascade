package runtime

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Purpose: the config.toml file-I/O and section-parsing helpers behind
//   Load (config.go): reading the raw tree, running the schema-upgrade
//   frame IN MEMORY ONLY (never persisted from a read path — R-14 CR
//   fix, P1-E03-W1-S05-T8), shape-validating [elevation], resolving the
//   active [runtime] profile, and splitting off the sections this ticket
//   does not own. Split out of config.go per R-14.117 (Art.10.3 file-cap
//   remedy) — behaviour-preserving, moved code only.
// Inputs: same as Load's — a config.toml path, the decoded generic tree,
//   the --profile flag, and injected Getenv/Warn accessors.
// Outputs: the parsed tree plus its per-key SourceFile annotations, typed
//   *ConfigError failures for malformed/invalid input, and the resolved
//   [runtime]/[elevation] section values Load assembles into *Config.
// Constraints: Art.7.1 — no bare $HOME access; callers always pass a
//   resolved path. Art.1 — extraSections preserves every section this
//   ticket does not own, unvalidated, unwarned. A read path (Load, and
//   therefore every `cascade config get`/`list`/`validate` and daemon
//   boot) NEVER writes to path — see readAndUpgradeTree's doc comment.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// readAndUpgradeTree reads path (if it exists), decodes it as TOML into a
// generic tree, records SourceFile for every leaf it contains, and runs
// the schema_version upgrade-rewrite frame (08-INIT-CONFIG-SPEC §3 point
// 4) IN MEMORY ONLY. A missing file is not an error — Load never creates
// config.toml as a side effect.
//
// R-14 CR FINDING (P1-E03-W1-S05-T8, blocking fix 1): this function used
// to call writeConfigAtomic(path, tree) whenever UpgradeSchema reported
// Mutated, round-tripping the file through toml.Marshal. Every read verb
// (get/list) and daemon boot funnel through Load → readAndUpgradeTree, so
// a purely-read operation on any legacy config.toml (one predating
// schema_version) silently rewrote the entire file on disk: comments
// gone, quote style canonicalised, keys re-sorted — exactly what
// docs/config-reference.md's "Round-trip fidelity" section promises never
// happens, and precisely the toml.Marshal landmine that section's own
// rationale calls out as "worse" than the alternative it chose. A
// read-only verb must never mutate the user's file, full stop.
//
// The fix: UpgradeSchema still runs (tree is stamped with the current
// schema_version so callers see the upgraded value for this call), but
// the result is never persisted here. Persisting a schema upgrade is
// deliberately NOT implemented anywhere in this ticket's scope: no
// migrationStep has ever existed (schema.go's migrationSteps is empty —
// schema 1 is the only generation P1 has shipped), so there is nothing a
// persisted rewrite would actually preserve that isn't already
// reconstructed identically, in memory, on every Load. The day a real
// schema 2 migration lands, the ticket that adds it must persist the
// upgrade — if it decides to — only from an explicit WRITE verb (`config
// set`/`unset`), through the structure-preserving line editor
// (toml_edit.go's SetKeyLine), never through toml.Marshal. This file
// intentionally no longer contains a toml.Marshal-based write path for
// that future ticket to accidentally reuse.
func readAndUpgradeTree(path string) (map[string]interface{}, map[string]ConfigSource, error) {
	tree := map[string]interface{}{}
	sources := map[string]ConfigSource{}
	if path == "" {
		tree["schema_version"] = int64(CurrentSchemaVersion)
		return tree, sources, nil
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if decErr := toml.Unmarshal(data, &tree); decErr != nil {
			return nil, nil, &ConfigError{Reason: fmt.Sprintf("malformed TOML in %s: %v", path, decErr)}
		}
		markSources(tree, "", sources, SourceFile)
	case os.IsNotExist(err):
		tree["schema_version"] = int64(CurrentSchemaVersion)
		return tree, sources, nil
	default:
		return nil, nil, fmt.Errorf("runtime: read config %s: %w", path, err)
	}

	if _, err := UpgradeSchema(tree); err != nil {
		return nil, nil, err
	}
	return tree, sources, nil
}

// parseElevationSection type-checks and shape-validates tree's [elevation]
// table. Cold-only, strict (T-1 task 6): an unrecognised key or a type
// mismatch is a hard typed error, never a warning.
func parseElevationSection(tree map[string]interface{}) (elevationSection, error) {
	raw, _ := tree["elevation"].(map[string]interface{})
	for k := range raw {
		if k != "allow_remote" && k != "helper_pubkey" {
			return elevationSection{}, &ConfigError{Field: "elevation." + k, Reason: "unrecognised key in [elevation]"}
		}
	}
	elevation := elevationSection{}
	if v, ok := raw["allow_remote"]; ok {
		b, ok := v.(bool)
		if !ok {
			return elevationSection{}, &ConfigError{Field: "elevation.allow_remote", Reason: "must be a boolean"}
		}
		elevation.AllowRemote = b
	}
	if v, ok := raw["helper_pubkey"]; ok {
		s, ok := v.(string)
		if !ok {
			return elevationSection{}, &ConfigError{Field: "elevation.helper_pubkey", Reason: "must be a string"}
		}
		elevation.HelperPubkey = s
	}
	if elevation.AllowRemote && elevation.HelperPubkey == "" {
		return elevationSection{}, &ConfigError{Field: "elevation.helper_pubkey", Reason: "required when elevation.allow_remote is true"}
	}
	return elevation, nil
}

// resolveRuntimeSection warns on unrecognised [runtime] keys (this ticket
// owns [runtime] but 08 leaves room for later additive keys — unlike
// [elevation], that is never a hard error) and resolves the active
// profile through ResolveProfile's flag > env > file > default cascade.
// home/data_dir are never read from the file; see runtimeSection's doc.
func resolveRuntimeSection(tree map[string]interface{}, flag string, getenv Getenv, warn func(string, ...interface{})) (Profile, ConfigSource, error) {
	raw, _ := tree["runtime"].(map[string]interface{})
	for k := range raw {
		switch k {
		case "profile":
		case "home", "data_dir":
			// Derived from the path provider and CASCADE_HOME, never from the
			// file, so a value here has no effect. Say so rather than
			// discarding it in silence, which reads as if it had been applied.
			warn("runtime: runtime.%s in config.toml is ignored; it is derived from the path layout (set CASCADE_HOME instead)", k)
		default:
			warn("runtime: unknown key runtime.%s in config.toml (preserved, not validated)", k)
		}
	}
	fileProfile, _ := raw["profile"].(string)
	return ResolveProfile(flag, func(k string) (string, bool) {
		v := getenv(k)
		return v, v != ""
	}, fileProfile)
}

// extraSections returns every top-level section of tree other than
// schema_version, runtime, and elevation, exactly as decoded, with no
// warning: these are valid future 08 §3 sections this ticket does not
// own (logging, storage, retrieval, ...).
func extraSections(tree map[string]interface{}) map[string]interface{} {
	extra := map[string]interface{}{}
	for k, v := range tree {
		switch k {
		case "schema_version", "runtime", "elevation":
			continue
		default:
			extra[k] = v
		}
	}
	return extra
}
