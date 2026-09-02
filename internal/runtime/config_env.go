package runtime

import (
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
// never treated as CASCADE_<SECTION>__<KEY> overrides.
var reservedEnvVars = map[string]bool{
	"CASCADE_HOME":      true,
	"CASCADE_PROFILE":   true,
	"CASCADE_CONFIG":    true,
	"CASCADE_SOCKET":    true,
	"CASCADE_NO_INPUT":  true,
	"CASCADE_YES":       true,
	"CASCADE_TELEMETRY": true,
}

// collectEnvOverrides scans environ for CASCADE_<SECTION>__<KEY...>
// variables (08-INIT-CONFIG-SPEC §2: "__" maps to "."), parses each value
// as a TOML literal, and returns a dotted-path -> value map.
func collectEnvOverrides(environ []string) map[string]interface{} {
	overrides := map[string]interface{}{}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "CASCADE_") {
			continue
		}
		if reservedEnvVars[name] || strings.HasPrefix(name, "CASCADE_INIT_") {
			continue
		}
		suffix := strings.TrimPrefix(name, "CASCADE_")
		segments := strings.Split(suffix, "__")
		if len(segments) < 2 {
			continue // not a section__key override
		}
		dotted := make([]string, len(segments))
		for i, s := range segments {
			dotted[i] = strings.ToLower(s)
		}
		overrides[strings.Join(dotted, ".")] = parseEnvLiteral(val)
	}
	return overrides
}

// parseEnvLiteral parses raw as a TOML value literal (true, 42, 1.5,
// "s", ["a","b"]); a bareword that is not valid TOML (e.g. an unquoted
// CASCADE_LOGGING__LEVEL=debug) falls back to a plain string.
func parseEnvLiteral(raw string) interface{} {
	var holder struct {
		V interface{} `toml:"v"`
	}
	if err := toml.Unmarshal([]byte("v = "+raw), &holder); err == nil {
		return holder.V
	}
	return raw
}
