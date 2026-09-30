package runtime

// Purpose: regression tests for the P1-PLG-01 fix-3 review findings on the
//   shared value screen (vetLiteral): the clean-absolute-path exemption
//   from the opaque-run check applies only at a path-typed key (last
//   segment ending _dir or _path), so every other key keeps the
//   pre-ticket refusal of an opaque run that starts with '/' (fix 1); and
//   a known-prefix key hidden by a Default_Ignorable rune that is not Cf
//   (variation selector, CGJ, Hangul filler) is refused (fix 2). Every
//   case runs through both Set and ApplyDiff, which share the one validator.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: credential-shaped literals are built by concatenation
//   (C22); every refusal asserts the file is byte-identical afterwards;
//   every acceptance asserts the value reached the file.
// SPORT: internal/runtime config_diff.go (TEST) — P1-PLG-01.

import (
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// keytypeOpaque40 is a 40-char run of the base64 alphabet: the opaque-token
// shape LooksLikeSecret refuses on its own.
var keytypeOpaque40 = strings.Repeat("q7Zp2Lx9Rt", 2) + strings.Repeat("Wm1Hy5Gs0J", 2)

// TestConfigRefusesOpaqueAbsPathAtNonPathKey: at a key that is not
// path-typed, a value shaped like a clean absolute path gets no exemption,
// so an opaque run or a key after "token=" is refused by both writers.
func TestConfigRefusesOpaqueAbsPathAtNonPathKey(t *testing.T) {
	for name, value := range map[string]string{
		"slash opaque40":   "/" + keytypeOpaque40,
		"x opaque40":       "/x/" + keytypeOpaque40,
		"x token= sk-live": "/x/token=" + "sk-" + "live-" + "abcdefghijklmnopqrstuvwxyz012345",
	} {
		t.Run(name, func(t *testing.T) {
			assertBothRefuse(t, "plugins.cascade-nself.postgres_host", tomlBasicString(value))
		})
	}
}

// TestConfigWritesPlainDirAtPathKeys: a long plain directory under
// t.TempDir() (opaque-token shaped on macOS and Linux) is written by both
// writers at a key ending _path or _dir, and at project_dir.
func TestConfigWritesPlainDirAtPathKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acamarata", "cascade-fixture", "data")
	if goruntime.GOOS != "windows" && !bareBase64Pattern.MatchString(dir) {
		t.Fatalf("fixture %q is not opaque-token shaped; the test would prove nothing", dir)
	}
	lit := tomlBasicString(dir)
	for _, key := range []string{"data_path", "cache_dir", "project_dir"} {
		t.Run(key, func(t *testing.T) {
			assertBothWrite(t, "plugins.cascade-nself."+key, lit, lit)
		})
	}
}

// TestConfigRefusesDefaultIgnorableSplitSecret: a known-prefix key cut by
// a Default_Ignorable rune outside Cf (U+FE0F, U+034F, U+3164) is refused
// by both writers.
func TestConfigRefusesDefaultIgnorableSplitSecret(t *testing.T) {
	for name, value := range map[string]string{
		"variation selector": "s️" + screenKey[1:],
		"cgj":                "sk͏" + screenKey[2:],
		"hangul filler":      "sㅤ" + screenKey[1:],
	} {
		t.Run(name, func(t *testing.T) {
			assertBothRefuse(t, "plugins.cascade-nself.postgres_host", tomlBasicString(value))
		})
	}
}
