package runtime

import (
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Purpose: generic tree-manipulation helpers and the
//   CASCADE_<SECTION>__<KEY> env-override machinery behind Load
//   (config.go): dotted-path source annotation, dotted-path flatten/set,
//   the reserved-env-var denylist, and env-override collection/literal
//   parsing. Split out of config.go per R-14.117 (Art.10.3 file-cap
//   remedy) — behaviour-preserving, moved code only.
// Inputs: the decoded generic config tree, and os.Environ()-shaped
//   environment slices.
// Outputs: dotted-path -> ConfigSource / value maps used to build
//   *Config's sources map, Extra view, and EffectiveEntries.
// Constraints: reservedEnvVars names are never treated as generic
//   CASCADE_<SECTION>__<KEY> overrides — they have dedicated,
//   non-generic meanings elsewhere in this package.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// markSources records src for every leaf key in tree, dotted-path style.
func markSources(tree map[string]interface{}, prefix string, sources map[string]ConfigSource, src ConfigSource) {
	for k, v := range tree {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]interface{}); ok {
			markSources(sub, key, sources, src)
			continue
		}
		sources[key] = src
	}
}

// flattenTree writes every leaf of tree into out, dotted-path style.
func flattenTree(tree map[string]interface{}, prefix string, out map[string]interface{}) {
	for k, v := range tree {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]interface{}); ok {
			flattenTree(sub, key, out)
			continue
		}
		out[key] = v
	}
}

// treeSet sets value at the dotted path in tree, creating intermediate
// tables as needed.
func treeSet(tree map[string]interface{}, dotted string, value interface{}) {
	parts := strings.Split(dotted, ".")
	m := tree
	for i, p := range parts {
		if i == len(parts)-1 {
			m[p] = value
			return
		}
		next, ok := m[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[p] = next
		}
		m = next
	}
}

// reservedEnvVars are CASCADE_* names with dedicated, non-generic
// meanings (path resolution, profile, init-wizard intake, ...). They are
// never treated as CASCADE_<SECTION>__<KEY> overrides. The set is gated
// against the names the tree reads by TestReservedEnvVarsCoverTree.
var reservedEnvVars = map[string]bool{
	"CASCADE_HOME":      true,
	"CASCADE_PROFILE":   true,
	"CASCADE_CONFIG":    true,
	"CASCADE_SOCKET":    true,
	"CASCADE_NO_INPUT":  true,
	"CASCADE_YES":       true,
	"CASCADE_TELEMETRY": true,

	// Completion-hook identifiers are payload fields, not config overrides.
	"CASCADE_SESSION_ID": true,
	"CASCADE_JOB_ID":     true,
	"CASCADE_TICKET_ID":  true,

	// Read by the node-upgrade RPC as the minisign trust key.
	"CASCADE_MINISIGN_PUBKEY": true,

	// A GitHub API token (secret-bearing), read from a name slice by the
	// github plugin and wait-on-green; declared in dynamicEnvReads.
	"CASCADE_GITHUB_TOKEN": true,

	// Read through a named string const (TestReservedEnvVarsCoverTree
	// resolves those through go/types).
	"CASCADE_BACKUP_AGE_IDENTITY":         true,
	"CASCADE_BACKUP_MANIFEST_SIGNING_KEY": true,
	"CASCADE_IDENTIFIER_PATTERNS":         true,
	"CASCADE_IDENTIFIER_PATTERNS_FILE":    true,
	"CASCADE_MEMORY_REVIEW_ACTION":        true,
	"CASCADE_TESTKIT_UPDATE_GOLDEN":       true,
	"CASCADE_HYGIENE_SWEEP_RANGE":         true,
	"CASCADE_HYGIENE_COMMIT_RANGE":        true,

	// Server-profile env refs: const names handed to ResolveDSNEnvRef, and
	// the four names ResolveS3EnvRefs builds from CASCADE_STORAGE_S3.
	"CASCADE_STORAGE_POSTGRES_DSN": true,
	"CASCADE_STORAGE_PGVECTOR_DSN": true,
	"CASCADE_STORAGE_REDIS_URL":    true,
	"CASCADE_STORAGE_S3_ENDPOINT":  true,
	"CASCADE_STORAGE_S3_BUCKET":    true,
	"CASCADE_STORAGE_S3_KEY_ID":    true,
	"CASCADE_STORAGE_S3_SECRET":    true,
}

// isReservedEnvName reports whether name is a dedicated CASCADE_* variable
// (reserved, or in the CASCADE_INIT_ intake family).
func isReservedEnvName(name string) bool {
	return reservedEnvVars[name] || strings.HasPrefix(name, "CASCADE_INIT_")
}

// envTypedSections are top-level sections parsed by their own typed
// parsers that knownConfigKeys does not enumerate key by key; an override
// under them is not reported as unknown.
var envTypedSections = map[string]bool{"fleet": true, "ci": true}

// envKeyKnown reports whether the dotted override key names a config key.
func envKeyKnown(dotted string) bool {
	if _, err := ResolveDottedPath(dotted); err == nil {
		return true
	}
	top, _, _ := strings.Cut(dotted, ".")
	return envTypedSections[top]
}

// envOverrideKey maps CASCADE_<SECTION>__<KEY...> to its lowercase dotted
// path, or "" when name is not a section__key override.
func envOverrideKey(name string) string {
	segments := strings.Split(strings.TrimPrefix(name, "CASCADE_"), "__")
	if len(segments) < 2 {
		return ""
	}
	for i, s := range segments {
		segments[i] = strings.ToLower(s)
	}
	return strings.Join(segments, ".")
}

// collectEnvOverrides scans environ for CASCADE_<SECTION>__<KEY...>
// variables (08-INIT-CONFIG-SPEC §2: "__" maps to "."), parses each value
// as a TOML literal, and returns a dotted-path -> value map.
//
// warn (nil discards) receives at most one message per variable, naming
// it and never its value, covering: a value that is not a TOML literal and
// falls back to a plain string; a key that is not a known config key; two
// variables that lowercase to the same key (the last name in sorted order
// wins, deterministically). It is the caller's own callback, never shared.
func collectEnvOverrides(environ []string, warn func(string, ...interface{})) map[string]interface{} {
	if warn == nil {
		warn = func(string, ...interface{}) {}
	}
	vars := map[string]string{}
	byKey := map[string][]string{}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "CASCADE_") || isReservedEnvName(name) {
			continue
		}
		key := envOverrideKey(name)
		if key == "" {
			continue // not a section__key override
		}
		if _, dup := vars[name]; !dup {
			byKey[key] = append(byKey[key], name)
		}
		vars[name] = val
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	overrides := map[string]interface{}{}
	for _, key := range keys {
		names := byKey[key]
		sort.Strings(names)
		var plain []string // names whose value fell back to a plain string
		for _, n := range names {
			v, literal := parseEnvLiteral(vars[n])
			if !literal {
				plain = append(plain, n)
			}
			overrides[key] = v // sorted order: the last name wins
		}
		warnEnvKey(key, names, plain, warn)
	}
	return overrides
}

// warnEnvKey emits at most one message for one dotted key, so no variable
// is named in more than one warning per Load: a collision message carries
// the unknown-key and plain-string notes; a single variable's conditions
// are joined into one line.
func warnEnvKey(key string, names, plain []string, warn func(string, ...interface{})) {
	unknown := !envKeyKnown(key)
	if len(names) == 1 {
		warnEnvVar(names[0], key, unknown, len(plain) == 1, warn)
		return
	}
	note := ""
	if unknown {
		note += "; the key is also not a known config key"
	}
	if len(plain) > 0 {
		note += "; not a valid TOML literal, used as a plain string: " + strings.Join(plain, ", ")
	}
	warn("runtime: env overrides %s all map to config key %s; %s wins%s", strings.Join(names, ", "), key, names[len(names)-1], note)
}

// warnEnvVar reports one variable's unknown key and plain-string fallback
// in a single message (nothing when neither applies).
func warnEnvVar(name, key string, unknown, plain bool, warn func(string, ...interface{})) {
	var notes []string
	if unknown {
		notes = append(notes, "sets unknown config key "+key+" (applied anyway, not validated)")
	}
	if plain {
		notes = append(notes, "is not a valid TOML literal; using it as a plain string (quote it to silence this)")
	}
	if len(notes) > 0 {
		warn("runtime: env override %s %s", name, strings.Join(notes, " and "))
	}
}

// parseEnvLiteral parses raw as a TOML value literal (true, 42, 1.5,
// "s", ["a","b"]); a bareword that is not valid TOML (e.g. an unquoted
// CASCADE_LOGGING__LEVEL=debug) falls back to a plain string and reports
// literal=false so the caller can warn.
func parseEnvLiteral(raw string) (value interface{}, literal bool) {
	var holder struct {
		V interface{} `toml:"v"`
	}
	if err := toml.Unmarshal([]byte("v = "+raw), &holder); err == nil {
		return holder.V, true
	}
	return raw, false
}
