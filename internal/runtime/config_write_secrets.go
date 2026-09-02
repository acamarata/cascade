package runtime

// Purpose: the secret-pattern detector and whole-file Validate behind
//
//	`cascade config set`/`validate` — split out of config_write.go per
//	R-14.117/Art.10.3 (300-line file cap).
//
// Inputs: a candidate literal value (secret detector) or a decoded config
//
//	tree (Validate).
//
// Outputs: a *SecretLiteralError naming the match reason, or a *ConfigError/
//
//	*SchemaError from Validate.
//
// Constraints: SEAM NOTE (ticket contract, verbatim) — these inline
//
//	heuristics are the W1 stand-in; H/S-15.T3 owns the precision-first
//	secret detector and supersedes them when it lands.
//
// SPORT: runtime/config-write-verbs (ADD, placeholder per T-8 sport_updates).

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// secretBearerPrefixes are well-known credential-token prefixes. A literal
// value beginning with one of these is refused by SetHandler regardless of
// length.
var secretBearerPrefixes = []string{
	"sk-", "ghp_", "gho_", "ghs_", "github_pat_", "xoxb-", "xoxp-", "xoxa-", "ya29.",
	"AIza", "AKIA", "-----BEGIN ",
}

// bareBase64Pattern matches an unbroken run of base64 alphabet characters
// (>=40 chars, no whitespace) — the shape of an opaque token or key rather
// than prose.
var bareBase64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{40,}$`)

// SecretLiteralError reports a `cascade config set` value that matches a
// secret-pattern heuristic; the caller must redirect to `cascade vault
// set` instead of writing the literal into config.toml.
type SecretLiteralError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *SecretLiteralError) Error() string {
	return fmt.Sprintf("runtime: config set %s: value looks like a secret (%s); use `cascade vault set` instead", e.Field, e.Reason)
}

// LooksLikeSecret runs the W1 inline secret-pattern heuristics: a bare
// base64-shaped run of >=40 characters with no whitespace, a known bearer
// token prefix, or a PEM header. It is intentionally conservative (a few
// false positives redirected to `cascade vault set` cost nothing; a false
// negative writes a credential into a tracked/plaintext file). SEAM NOTE
// (ticket contract, verbatim): these inline heuristics are the W1
// stand-in — H/S-15.T3 owns the precision-first secret detector and
// supersedes them when it lands; W2 converges on one detector
// (06-FORGE-SPEC §5.19 allowed-fail precedent).
func LooksLikeSecret(value string) (bool, string) {
	// Bearer-prefix and PEM-header checks run unconditionally: a PEM
	// header ("-----BEGIN PRIVATE KEY-----") legitimately contains
	// spaces, so the whitespace guard below (which exists to keep prose
	// out of the bare-base64 check) must not short-circuit these two
	// first.
	for _, prefix := range secretBearerPrefixes {
		if strings.HasPrefix(value, prefix) {
			return true, fmt.Sprintf("matches known bearer-token prefix %q", prefix)
		}
	}
	if strings.Contains(value, "PRIVATE KEY-----") {
		return true, "matches a PEM private-key header"
	}
	if strings.ContainsAny(value, " \t\n\r") {
		return false, ""
	}
	if bareBase64Pattern.MatchString(value) {
		if _, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "=")); err == nil {
			return true, "looks like a bare base64-encoded token (>=40 chars)"
		}
		// Even a non-strict-base64 40+ char opaque run without whitespace
		// is treated as secret-shaped: real tokens are rarely valid
		// standard base64 either (mixed alphabets, no padding).
		return true, "looks like an opaque high-entropy token (>=40 chars, no whitespace)"
	}
	return false, ""
}

// checkLiteralForSecrets applies LooksLikeSecret to value when it is a
// string (the only shape a literal secret can take — bool/int/float/array
// elements are never credential-shaped in practice, and 08 §3 never
// stores a secret as a bare non-string scalar).
func checkLiteralForSecrets(field string, value interface{}) error {
	s, ok := value.(string)
	if !ok {
		return nil
	}
	if bad, reason := LooksLikeSecret(s); bad {
		return &SecretLiteralError{Field: field, Reason: reason}
	}
	return nil
}

// ScanTreeForSecrets walks every leaf of tree (dotted-path style, via
// flattenTree) and applies the same LooksLikeSecret heuristic
// checkLiteralForSecrets uses for a single `config set` literal, so any
// caller that accepts a whole document at once — not just one key at a
// time — gets the identical guard. Returns the first *SecretLiteralError
// found (map iteration order is unspecified, but tree round-trips
// deterministically for a given input document, and this is a refuse/
// don't-refuse decision, not a report needing every match).
//
// R-14 CR FINDING (P1-E03-W1-S05-T8, blocking fix 3): `cascade config
// set` refuses a secret-shaped literal via checkLiteralForSecrets, but
// `cascade config edit` (cmd/cascade/config/config_write.go) called only
// DecodeConfigFile + Validate, neither of which screens values — so a
// secret pasted through $EDITOR was written to config.toml in plaintext.
// Same value class, opposite outcome, purely because of which verb the
// user happened to type. ScanTreeForSecrets closes that gap: `edit`'s
// RunE now calls it on the decoded edited tree before writing, exactly
// like `set` already does for its one literal.
func ScanTreeForSecrets(tree map[string]interface{}) error {
	leaves := map[string]interface{}{}
	flattenTree(tree, "", leaves)
	keys := sortedTreeKeys(leaves)
	for _, k := range keys {
		if err := checkLiteralForSecrets(k, leaves[k]); err != nil {
			return err
		}
	}
	return nil
}

// sortedTreeKeys returns leaves' keys sorted, so ScanTreeForSecrets'
// first-match error is deterministic across runs (map iteration order is
// not) rather than depending on Go's randomised map order.
func sortedTreeKeys(leaves map[string]interface{}) []string {
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate whole-file-checks tree: a syntactically well-formed generic
// TOML document (the caller decoded it to get here) whose [runtime],
// [elevation], and [logging] sections — the only sections this package
// type-checks — parse cleanly. Every other 08 §3 section is accepted
// as-is (Art.1: this ticket never invents validation for a section it
// does not own; C/S-04.T1 never shipped a schema-driven Validate this
// ticket could call instead, so this is the real, working
// `cascade config validate` behind both the CLI verb and every
// write-then-validate path in toml_edit.go/hotreload.go).
func Validate(tree map[string]interface{}) error {
	if _, err := parseElevationSection(tree); err != nil {
		return err
	}
	if _, err := parseLoggingSection(tree, func(string, ...interface{}) {}); err != nil {
		return err
	}
	if from := schemaVersionOf(tree); from > CurrentSchemaVersion {
		return &SchemaError{FoundVersion: from, CurrentVersion: CurrentSchemaVersion}
	}
	return nil
}

// sortedKnownKeys returns a sorted copy of knownConfigKeys, exported for
// tests and for `cascade config list --known` style introspection.
func sortedKnownKeys() []string {
	out := append([]string(nil), knownConfigKeys...)
	sort.Strings(out)
	return out
}

// DecodeConfigFile reads and TOML-decodes path into a generic tree,
// tolerating a not-yet-existing file as an empty document — the same
// read semantics as Load's own readAndUpgradeTree, minus the
// schema-version upgrade/rewrite side effect, which `cascade config
// validate` (this function's caller, cmd/cascade/config) must never
// trigger: validating a file must never write to it.
func DecodeConfigFile(path string) (map[string]interface{}, error) {
	data, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	tree := map[string]interface{}{}
	if len(data) == 0 {
		return tree, nil
	}
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, &ConfigError{Reason: fmt.Sprintf("malformed TOML in %s: %v", path, err)}
	}
	return tree, nil
}
