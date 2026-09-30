package runtime

// Purpose: screenDiffValue, the value half of vetLiteral (the ONE
// validator Set, ApplyDiff and ScreenConfigLiteral share): it refuses a
// secret-shaped string, a URL carrying userinfo, a secret split across
// whitespace or hidden by an invisible rune, and a secret-shaped
// inline-table KEY, anywhere in a decoded literal.
// Inputs: the dotted path (for the error and the key type) and a decoded
// TOML value.
// Outputs: nil, or the first *SecretLiteralError.
// Constraints: LooksLikeSecret (config_write_secrets.go) stays the single
// shape detector; this file only decides what text it sees. Unicode
// format runes (Cf: ZWSP, ZWNJ, ZWJ, BOM, word joiner) are removed before
// any check, and so are the other Default_Ignorable runes (variation
// selectors, CGJ, Hangul fillers); Unicode spaces (unicode.IsSpace, which
// covers every Zs rune: NBSP, U+3000) separate fields. At a path-typed key
// only (last segment ending _dir or _path, project_dir included) a clean
// absolute filesystem path is exempt from the opaque-run check (a
// t.TempDir() is 40+ chars of the base64 alphabet); its prefix, PEM and
// userinfo checks still run. Every other key, and every inline-table key
// name, keeps the full detector.
// SPORT: internal/runtime config_diff.go family (ADD) — P1-PLG-01.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// urlUserinfo matches a URL authority carrying userinfo (user:pass@host).
var urlUserinfo = regexp.MustCompile(`://[^/?#]*@`)

// screenDiffValue screens every string in value (arrays and inline tables
// included) and every inline-table key with screenDiffString.
func screenDiffValue(path string, value interface{}) error {
	switch x := value.(type) {
	case string:
		return screenDiffString(path, x, isPathTypedKey(path))
	case []interface{}:
		for _, item := range x {
			if err := screenDiffValue(path, item); err != nil {
				return err
			}
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := screenDiffString(path, k, false); err != nil {
				return err
			}
			if err := screenDiffValue(path+"."+k, x[k]); err != nil {
				return err
			}
		}
	}
	return nil
}

// screenDiffString runs every check on s with its invisible runes
// removed: the shape detector (skipped for a clean absolute path when
// pathKey is set), userinfo, a bearer prefix at the start of any path
// segment or split field, and the shape detector on every
// whitespace-separated field.
func screenDiffString(path, s string, pathKey bool) error {
	visible := strings.Map(dropInvisibleRune, s)
	if err := screenShape(path, visible, pathKey); err != nil {
		return err
	}
	if urlUserinfo.MatchString(visible) {
		return &SecretLiteralError{Field: path, Reason: "a URL carrying userinfo (user:password@host)"}
	}
	if err := screenSegmentPrefixes(path, visible); err != nil {
		return err
	}
	return screenWhitespaceSplit(path, visible, pathKey)
}

// dropInvisibleRune removes a Unicode format rune (category Cf) or another
// Default_Ignorable_Code_Point (Other_Default_Ignorable_Code_Point: CGJ,
// Hangul fillers; Variation_Selector: U+FE0F), which is invisible and
// would otherwise hide a prefix or split an opaque run.
func dropInvisibleRune(r rune) rune {
	if unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) {
		return -1
	}
	return r
}

// isPathTypedKey reports whether the last segment of the dotted path
// names a filesystem location: it ends in _dir or _path (project_dir
// included). An unparseable path is not path-typed (fail closed).
func isPathTypedKey(path string) bool {
	segments, err := SplitDottedPath(path)
	if err != nil || len(segments) == 0 {
		return false
	}
	last := segments[len(segments)-1]
	return strings.HasSuffix(last, "_dir") || strings.HasSuffix(last, "_path")
}

// isFieldSeparator reports whether r separates fields: any Unicode space.
func isFieldSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.Is(unicode.Zs, r)
}

// isCleanAbsPath reports whether s is a clean absolute filesystem path
// with no URL scheme.
func isCleanAbsPath(s string) bool {
	return filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.Contains(s, "://")
}

// screenShape runs the shared detector (ScanTreeForSecrets) on s, except
// on a clean absolute path at a path-typed key, whose PEM and prefix
// checks run in screenSegmentPrefixes instead.
func screenShape(path, s string, pathKey bool) error {
	if pathKey && isCleanAbsPath(s) {
		if strings.Contains(s, "PRIVATE KEY-----") {
			return &SecretLiteralError{Field: path, Reason: "a path carrying a PEM private-key header"}
		}
		return nil
	}
	return ScanTreeForSecrets(map[string]interface{}{path: s})
}

// screenSegmentPrefixes refuses s when a path segment ('/' or '\'), a
// whitespace-separated field of one, or a segment's fields joined without
// whitespace (a prefix cut in two, "s k-live-…") starts with a known
// bearer-token prefix. The joined text is checked for prefixes only: a
// bare-base64 check on it would refuse ordinary paths with spaces.
func screenSegmentPrefixes(path, s string) error {
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		fields := strings.FieldsFunc(seg, isFieldSeparator)
		for _, f := range append(fields, strings.Join(fields, "")) {
			if hasBearerPrefix(f) {
				return &SecretLiteralError{Field: path, Reason: "a known bearer-token prefix inside the value"}
			}
		}
	}
	return nil
}

// hasBearerPrefix reports whether s starts with a secretBearerPrefixes
// entry (trailing space trimmed, so "-----BEGIN" matches too).
func hasBearerPrefix(s string) bool {
	for _, prefix := range secretBearerPrefixes {
		if strings.HasPrefix(s, strings.TrimSpace(prefix)) {
			return true
		}
	}
	return false
}

// screenWhitespaceSplit runs screenShape on every whitespace-separated
// part of s, so a secret after a word ("x sk-live-…") or an opaque run
// next to a space is refused like the bare value would be.
func screenWhitespaceSplit(path, s string, pathKey bool) error {
	fields := strings.FieldsFunc(s, isFieldSeparator)
	if len(fields) == 1 && fields[0] == s {
		return nil
	}
	for _, f := range fields {
		if err := screenShape(path, f, pathKey); err != nil {
			return &SecretLiteralError{Field: path, Reason: "a whitespace-separated part is secret-shaped"}
		}
	}
	return nil
}
