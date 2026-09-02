package runtime

// Purpose: the write-side primitives behind `cascade config set/unset` and
//
//	validate-before-write: a dotted-path segment validator with a
//	nearest-match suggestion over the known-key registry, a TOML-literal
//	value parser (bool/int/float/string/array), a secret-pattern detector
//	that redirects likely credential values to `cascade vault set`, and
//	Validate — the whole-file check every write path runs before any disk
//	mutation.
//
// Inputs: a dotted key string ("a.b.c"), a raw literal-value string
//
//	(`true`, `42`, `1.5`, `"s"`, `["a","b"]`), and the in-memory candidate
//	config tree Validate checks.
//
// Outputs: typed Go values from the literal parser; a *ConfigError (or a
//
//	*SecretLiteralError) naming the offending field on any failure; a
//	nearest-match key suggestion on an unknown dotted path.
//
// Constraints: Art.1 — this ticket does not invent shape validation for
//
//	sections it does not own (logging/elevation/runtime are the only
//	typed sections; everything else round-trips through Extra
//	unvalidated, matching config_load.go's existing policy). Disk is
//	never touched by anything in this file — toml_edit.go's editor calls
//	Validate before it writes, this file only decides yes/no.
//
// SPORT: runtime/config-write-verbs (ADD, placeholder per T-8 sport_updates).

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// knownConfigKeys is this ticket's own registry of dotted-path keys used
// for nearest-match suggestions on `cascade config set`/`get` of an
// unrecognised key. C/S-04.T1 does not ship a schema-driven registry (no
// Validate/knownKeys type exists anywhere in this package before this
// ticket), so this is a hand-maintained list sourced directly from
// 08-INIT-CONFIG-SPEC.md §3's section table (representative keys) plus the
// two typed sections this package already owns. It is intentionally not
// exhaustive over every plugin-namespaced key ([plugins.<name>].*, opaque
// per-manifest) — those are validated by the owning plugin's manifest, not
// here (Art.1: no invented validation for sections this ticket does not
// own).
var knownConfigKeys = []string{
	"schema_version",
	"runtime.profile", "runtime.home", "runtime.data_dir",
	"daemon.socket", "daemon.shutdown_grace",
	"logging.level", "logging.format",
	"logging.rotation.max_size_mb", "logging.rotation.max_files",
	"storage.driver",
	"retrieval.sources", "retrieval.fusion.k", "retrieval.fusion.weights",
	"retrieval.reranker.enabled",
	"memory.review_cadence",
	"policy.autonomy_profile", "policy.approval_batch_window_s", "policy.approval_batch_cap",
	"secrets.keychain_backend", "secrets.clipboard_ttl",
	"conductor.lane_order", "conductor.external_routing_enabled", "conductor.spill_enabled",
	"notify.aggregation_window",
	"nodes.trust_tier",
	"sync.class",
	"telemetry.enabled",
	"hooks.id", "hooks.trigger", "hooks.action_type", "hooks.action_params",
	"governor.sampler_hz", "governor.admission.queue_cap", "governor.admission.max_inflight",
	"governor.admission.compile_class_cap", "governor.admission.swap_threshold", "governor.preset",
	"registry.url", "registry.cache_dir", "registry.cache_ttl", "registry.pubkey_path",
	"plugins.enable", "plugins.disable", "plugins.harness", "plugins.enable_remote_runtime",
	"elevation.allow_remote", "elevation.helper_pubkey",
}

// bareKeyPattern matches a single valid TOML bare key segment (the subset
// this ticket accepts — quoted-key segments are out of scope for W1's
// dotted-path syntax per 08 §3's own examples, which are all bare keys).
var bareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DottedPathError reports an invalid dotted-path key: an empty segment, a
// segment that is not a valid TOML bare key, or (for `set`/`unset`) a key
// unrecognised in knownConfigKeys, with a nearest-match suggestion when one
// is close enough to be useful.
type DottedPathError struct {
	Path       string
	Reason     string
	Suggestion string
}

// Error implements the error interface.
func (e *DottedPathError) Error() string {
	if e.Suggestion != "" {
		return fmt.Sprintf("runtime: config key %q: %s (did you mean %q?)", e.Path, e.Reason, e.Suggestion)
	}
	return fmt.Sprintf("runtime: config key %q: %s", e.Path, e.Reason)
}

// SplitDottedPath splits dotted into its TOML key segments and validates
// each one is a syntactically valid bare key. It does not check the path
// against knownConfigKeys — callers that need that do so separately via
// ResolveDottedPath, so read-only callers (e.g. a future `get` on an
// as-yet-unknown key) are not forced through the suggestion machinery.
func SplitDottedPath(dotted string) ([]string, error) {
	if dotted == "" {
		return nil, &DottedPathError{Path: dotted, Reason: "empty key"}
	}
	segments := strings.Split(dotted, ".")
	for _, seg := range segments {
		if seg == "" {
			return nil, &DottedPathError{Path: dotted, Reason: "empty path segment"}
		}
		if !bareKeyPattern.MatchString(seg) {
			return nil, &DottedPathError{Path: dotted, Reason: fmt.Sprintf("invalid TOML key segment %q", seg)}
		}
	}
	return segments, nil
}

// ResolveDottedPath validates dotted as a well-formed path AND checks it
// against knownConfigKeys, returning a *DottedPathError carrying a
// nearest-match Suggestion when the key is unrecognised (`cascade config
// set` acceptance criterion: unknown key -> nearest-match suggestion, no
// write).
func ResolveDottedPath(dotted string) ([]string, error) {
	segments, err := SplitDottedPath(dotted)
	if err != nil {
		return nil, err
	}
	for _, known := range knownConfigKeys {
		if known == dotted {
			return segments, nil
		}
		// A key one level under a known table-valued key (e.g. an
		// as-yet-uncataloged plugins.<name> or hooks.<id> entry) is still
		// accepted verbatim: the registry lists representative keys, not
		// every possible instance.
		if strings.HasPrefix(dotted, known+".") {
			return segments, nil
		}
	}
	return nil, &DottedPathError{Path: dotted, Reason: "unknown config key", Suggestion: nearestKnownKey(dotted)}
}

// nearestKnownKey returns the entry of knownConfigKeys with the smallest
// Levenshtein distance to dotted, or "" if knownConfigKeys is empty.
func nearestKnownKey(dotted string) string {
	best := ""
	bestDist := -1
	for _, known := range knownConfigKeys {
		d := levenshtein(dotted, known)
		if bestDist == -1 || d < bestDist {
			bestDist = d
			best = known
		}
	}
	return best
}

// levenshtein computes the classic edit distance between a and b (single
// backing row, O(min(len)) memory).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = minInt(minInt(del, ins), sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// LiteralError reports a TOML-literal value string that could not be
// parsed as a scalar or array, with a format hint for the caller.
type LiteralError struct {
	Raw  string
	Hint string
}

// Error implements the error interface.
func (e *LiteralError) Error() string {
	return fmt.Sprintf("runtime: invalid TOML literal %q: %s", e.Raw, e.Hint)
}

// ParseTomlLiteral parses raw as a TOML value literal: bool (true/false),
// integer, float, quoted string, or array (["a","b"]). Unlike
// config_env.go's parseEnvLiteral (which silently falls back to treating
// an unparseable bareword as a plain string — correct for its own
// bareword-friendly env-var use case), this returns a typed *LiteralError
// with a format hint on failure: `cascade config set` must refuse an
// invalid literal outright rather than writing a wrong value.
func ParseTomlLiteral(raw string) (interface{}, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, &LiteralError{Raw: raw, Hint: "empty value; use a TOML literal like true, 42, 1.5, \"text\", or [\"a\",\"b\"]"}
	}
	var holder struct {
		V interface{} `toml:"v"`
	}
	if err := toml.Unmarshal([]byte("v = "+trimmed), &holder); err != nil {
		return nil, &LiteralError{Raw: raw, Hint: "not a valid TOML literal (bool/int/float/quoted string/array); wrap bare strings in double quotes"}
	}
	return holder.V, nil
}
