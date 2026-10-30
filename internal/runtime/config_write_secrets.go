package runtime

// Purpose: the config secret screen (LooksLikeSecret, the injected
// SecretLiteralScanner, ScanTreeForSecrets) and Validate (P1-SEC-30, AUD-027).
// Outputs: *SecretLiteralsError with every hit, or KindUnavailable with no scanner.
// Constraints: fail closed; errors name fields, key positions and classes, never
// values or secret key names. Every f688c0b LooksLikeSecret refusal stays.
// SPORT: runtime/config-write-verbs (P1-SEC-30, AUD-027).

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

// secretBearerPrefixes are well-known credential-token prefixes.
var secretBearerPrefixes = []string{
	"sk-", "ghp_", "gho_", "ghs_", "github_pat_", "xoxb-", "xoxp-", "xoxa-", "ya29.", "AIza", "AKIA", "-----BEGIN ",
}

// bareBase64Pattern matches an unbroken base64-alphabet run of >=40 chars.
var bareBase64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{40,}$`)

// SecretLiteralError is one secret-shaped field: redirect to `cascade vault set`.
type SecretLiteralError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *SecretLiteralError) Error() string {
	return fmt.Sprintf("runtime: config set %s: value looks like a secret (%s); use `cascade vault set` instead", e.Field, e.Reason)
}

// LooksLikeSecret is the f688c0b heuristic: a bearer prefix, a PEM private-key
// header, or a >=40-char base64-alphabet run without whitespace.
func LooksLikeSecret(value string) (bool, string) {
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
		return true, "looks like an opaque high-entropy token (>=40 chars, no whitespace)"
	}
	return false, ""
}

// SecretLiteralScanner returns the credential class names detected in value, never the value.
type SecretLiteralScanner func(value string) []string

// literalScanner is nil until cmd/cascade's init installs the detector adapter.
var literalScanner atomic.Pointer[SecretLiteralScanner]

// SetSecretLiteralScanner installs s for every config value write; nil uninstalls (every scan refuses).
func SetSecretLiteralScanner(s SecretLiteralScanner) {
	if s == nil {
		literalScanner.Store(nil)
		return
	}
	literalScanner.Store(&s)
}

// SecretLiteralsError reports every secret-shaped field of one write, sorted; never values.
type SecretLiteralsError struct {
	Hits []SecretLiteralError
}

// Error implements the error interface.
func (e *SecretLiteralsError) Error() string {
	parts := make([]string, len(e.Hits))
	for i, h := range e.Hits {
		parts[i] = fmt.Sprintf("%s (%s)", h.Field, h.Reason)
	}
	return fmt.Sprintf("runtime: config values look like secrets at %d field(s): %s; use `cascade vault set` instead", len(e.Hits), strings.Join(parts, "; "))
}

// Unwrap exposes each hit, so errors.As to *SecretLiteralError still matches.
func (e *SecretLiteralsError) Unwrap() []error {
	out := make([]error, len(e.Hits))
	for i := range e.Hits {
		out[i] = &e.Hits[i]
	}
	return out
}

// pieceSeparators split a value into pieces scanned alone (host:<key>, /d/token=<key>).
const pieceSeparators = "/\\:@=?#&,;\"'()[]<>|"

// userinfoNoScheme matches user:password@host with or without a scheme.
var userinfoNoScheme = regexp.MustCompile(`[^\s:/@]+:[^\s:/@]{4,}@[^\s:/@]+`)

// joinRun is a possible part of a key split across two adjacent leaves.
var joinRun = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ScanTreeForSecrets screens every string in tree (array elements too) with
// LooksLikeSecret and the installed scanner, on the value, its folded form
// and every piece, and scans adjacent alphanumeric leaves of one table or
// array joined. It returns *SecretLiteralsError with every hit sorted by
// field, or KindUnavailable when no scanner is installed.
func ScanTreeForSecrets(tree map[string]interface{}) error {
	scanp := literalScanner.Load()
	if scanp == nil {
		return cascade.New(cascade.KindUnavailable, "runtime: no secret scanner is installed; refusing to write config values")
	}
	scan, leaves := *scanp, map[string]interface{}{}
	hits := flattenScreened(scan, tree, "", leaves)
	var prevKey, prevRun string // prevRun is "" when the previous leaf already hit
	for _, k := range sortedTreeKeys(leaves) {
		n, strs := len(hits), leafStrings(leaves[k], nil)
		for _, s := range strs {
			if reason := literalSecretReason(scan, s); reason != "" {
				hits = append(hits, SecretLiteralError{Field: k, Reason: reason})
				break
			}
		}
		for i := 1; i < len(strs) && len(hits) == n; i++ {
			if joinedLeak(scan, strs[i-1], strs[i]) {
				hits = append(hits, SecretLiteralError{Field: k, Reason: "adjacent array elements join into a secret-shaped value"})
			}
		}
		run := ""
		if s, ok := leaves[k].(string); ok && len(hits) == n && !isPathTypedKey(k) {
			run = s
		}
		if prevKey != "" && parentPath(prevKey) == parentPath(k) && joinedLeak(scan, prevRun, run) {
			hits = append(hits, SecretLiteralError{Field: k, Reason: "joined with " + prevKey + " forms a secret-shaped value"})
		}
		prevKey, prevRun = k, run
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Field < hits[j].Field })
	return &SecretLiteralsError{Hits: hits}
}

// leafStrings collects every string inside v (arrays and inline tables).
func leafStrings(v interface{}, out []string) []string {
	switch x := v.(type) {
	case string:
		out = append(out, x)
	case []interface{}:
		for _, item := range x {
			out = leafStrings(item, out)
		}
	case map[string]interface{}:
		for _, k := range sortedTreeKeys(x) { // an inline-table key is screened like a value
			out = leafStrings(x[k], append(out, k))
		}
	}
	return out
}

// flattenScreened is flattenTree that also screens every key name: a refused
// name is reported as "<key N>" (its 1-based sorted index) and never printed.
func flattenScreened(scan SecretLiteralScanner, tree map[string]interface{}, prefix string, out map[string]interface{}) (hits []SecretLiteralError) {
	for i, k := range sortedTreeKeys(tree) {
		path, secret := k, literalSecretReason(scan, k) != ""
		if secret {
			path = fmt.Sprintf("<key %d>", i+1)
		}
		if prefix != "" {
			path = prefix + "." + path
		}
		if secret {
			hits = append(hits, SecretLiteralError{Field: path, Reason: "the key name looks like a secret"})
		}
		if sub, ok := tree[k].(map[string]interface{}); ok {
			hits = append(hits, flattenScreened(scan, sub, path, out)...)
		} else if _, dup := out[path]; dup {
			hits = append(hits, SecretLiteralError{Field: path, Reason: "two keys flatten to the same path"})
		} else {
			out[path] = tree[k]
		}
	}
	return hits
}

// literalSecretReason returns why s is refused, or "".
func literalSecretReason(scan SecretLiteralScanner, s string) string {
	folded := foldConfusables(s)
	for _, v := range []string{s, folded} {
		if bad, reason := LooksLikeSecret(v); bad {
			return reason
		}
	}
	if userinfoNoScheme.MatchString(folded) {
		return "carries userinfo (user:password@host)"
	}
	if classes := scan(folded); len(classes) > 0 {
		return "detected " + strings.Join(classes, ", ")
	}
	pieces := strings.FieldsFunc(folded, isPieceSeparator)
	for i, piece := range pieces {
		if piece == folded {
			continue
		}
		if classes := scan(piece); len(classes) > 0 {
			return "a part of the value: detected " + strings.Join(classes, ", ")
		}
		if i > 0 && joinedLeak(scan, pieces[i-1], piece) {
			return "two adjacent parts join into a secret-shaped value"
		}
	}
	return ""
}

// joinedLeak: two fragments that join into a known prefix, or two 8+ runs the scanner flags.
func joinedLeak(scan SecretLiteralScanner, a, b string) bool {
	if !joinRun.MatchString(a) || !joinRun.MatchString(b) {
		return false
	}
	if hasBearerPrefix(a + b) {
		return true
	}
	return len(a) >= 8 && len(b) >= 8 && !strings.ContainsAny(a+b, "-.") && len(scan(a+b)) > 0
}

// isPieceSeparator splits on whitespace and pieceSeparators.
func isPieceSeparator(r rune) bool {
	return isFieldSeparator(r) || strings.ContainsRune(pieceSeparators, r)
}

// foldConfusables maps fullwidth ASCII and U+3000 to ASCII and drops invisible runes.
func foldConfusables(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return dropInvisibleRune(r)
	}, s)
}

// parentPath is the dotted table that holds key ("" at the top level).
func parentPath(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[:i]
	}
	return ""
}

// sortedTreeKeys returns leaves' keys sorted (deterministic hit order).
func sortedTreeKeys(leaves map[string]interface{}) []string {
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate type-checks [elevation] and [logging] and refuses a newer schema.
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

// DecodeConfigFile decodes path (missing = empty) without Load's upgrade rewrite.
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
