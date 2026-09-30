package runtime

// Purpose: regression tests for the P1-PLG-01 fix-2 review findings on the
//   shared value screen (vetLiteral): a plain absolute directory is not an
//   opaque token (fix 1), a key hidden by a zero-width or other format
//   rune, NBSP or ideographic space is refused (fix 2), and an inline-table
//   KEY is screened like a value (fix 3). Every case runs through both Set
//   and ApplyDiff, which share the one validator.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: credential-shaped literals are built by concatenation
//   (C22); every refusal asserts the file is byte-identical afterwards;
//   every acceptance asserts the value reached the file.
// SPORT: internal/runtime config_diff.go (TEST) — P1-PLG-01.

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// screenKey is a known-prefix key LooksLikeSecret refuses on its own.
const screenKey = "sk-" + "live-" + "abcdefghijklmnopqrstuvwx"

// assertBothRefuse fails unless Set and ApplyDiff both refuse lit at path
// with a *SecretLiteralError and leave the file byte-identical.
func assertBothRefuse(t *testing.T, path, lit string) {
	t.Helper()
	w := writerAt(t, guardSeed)
	_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{{Path: path, Literal: lit}}})
	assertKind(t, err, cascade.KindPolicyDenied)
	var secret *SecretLiteralError
	if !errors.As(err, &secret) {
		t.Fatalf("ApplyDiff(%q) err = %v, want *SecretLiteralError", lit, err)
	}
	assertUnchanged(t, w)
	if _, err := w.Set(path, lit); !errors.As(err, &secret) {
		t.Fatalf("Set(%q) err = %v, want *SecretLiteralError", lit, err)
	}
	assertUnchanged(t, w)
}

// assertBothWrite fails unless Set and ApplyDiff both write lit at path
// and the file then carries want.
func assertBothWrite(t *testing.T, path, lit, want string) {
	t.Helper()
	w := writerAt(t, guardSeed)
	res, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{{Path: path, Literal: lit}}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("ApplyDiff(%q) = (%+v, %v), want it applied", lit, res, err)
	}
	assertFileHas(t, w, want)
	w = writerAt(t, guardSeed)
	if _, err := w.Set(path, lit); err != nil {
		t.Fatalf("Set(%q): %v", lit, err)
	}
	assertFileHas(t, w, want)
}

// assertFileHas fails unless w.Path contains want (want non-empty).
func assertFileHas(t *testing.T, w *ConfigWriter, want string) {
	t.Helper()
	data, err := os.ReadFile(w.Path)
	if err != nil || want == "" || !strings.Contains(string(data), want) {
		t.Fatalf("config.toml (%v) lacks %q:\n%s", err, want, data)
	}
}

// TestConfigWritesBareAbsoluteDir: a bare t.TempDir()-based directory, and
// a long dotless one under it, are written by both writers. On macOS and
// Linux each is a 40+ char run of [A-Za-z0-9+/=_-], the opaque-token shape
// a clean absolute path is now exempt from.
func TestConfigWritesBareAbsoluteDir(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{base, filepath.Join(base, "acamarata", "cascade-fixture", "nself-app")} {
		if goruntime.GOOS != "windows" && !bareBase64Pattern.MatchString(dir) {
			t.Fatalf("fixture %q is not opaque-token shaped; the test would prove nothing", dir)
		}
		lit := tomlBasicString(dir)
		assertBothWrite(t, "plugins.cascade-nself.project_dir", lit, lit)
	}
}

// TestConfigRefusesSecretInAbsolutePath: the path exemption covers the
// opaque-run check only; a bearer prefix in any segment, a PEM header
// and a zero-width rune inside the path are still refused.
func TestConfigRefusesSecretInAbsolutePath(t *testing.T) {
	for name, value := range map[string]string{
		"literal /x/ segment":  "/x/" + screenKey,
		"temp dir segment":     filepath.Join(t.TempDir(), "x", screenKey),
		"github segment":       filepath.Join(t.TempDir(), "gh"+"p_"+strings.Repeat("A", 36)),
		"pem header":           filepath.Join(t.TempDir(), "-----BEGIN RSA PRIVATE"+" KEY-----"),
		"zero-width in prefix": filepath.Join(t.TempDir(), "s\u200bk-"+"live-"+"abcdefghijkl"),
	} {
		t.Run(name, func(t *testing.T) {
			assertBothRefuse(t, "plugins.cascade-nself.project_dir", tomlBasicString(value))
		})
	}
}

// TestConfigRefusesFormatRuneSplitSecret: a key hidden by a zero-width
// space, ZWNJ, ZWJ, BOM or word joiner (Unicode Cf), or by NBSP or an
// ideographic space (Zs), is refused by both writers.
func TestConfigRefusesFormatRuneSplitSecret(t *testing.T) {
	for name, value := range map[string]string{
		"zwsp leading":    "\u200b" + screenKey,
		"zwsp in prefix":  "s\u200b" + screenKey[1:],
		"zwnj in prefix":  "sk\u200c" + screenKey[2:],
		"zwj in prefix":   "sk\u200d" + screenKey[2:],
		"bom leading":     "\ufeff" + screenKey,
		"word joiner":     "s\u2060" + screenKey[1:],
		"nbsp in prefix":  "s\u00a0" + screenKey[1:],
		"ideographic cut": "s\u3000" + screenKey[1:],
		"zwsp opaque run": strings.Repeat("q7Zp2Lx9Rt", 2) + "\u200b" + strings.Repeat("Wm1Hy5Gs0J", 2),
	} {
		t.Run(name, func(t *testing.T) {
			assertBothRefuse(t, "plugins.cascade-nself.postgres_host", tomlBasicString(value))
		})
	}
}

// TestConfigRefusesSecretInlineTableKey: an inline-table key is screened
// the same way as a value, at any depth and behind a zero-width rune.
func TestConfigRefusesSecretInlineTableKey(t *testing.T) {
	for name, lit := range map[string]string{
		"top-level key": `{"` + screenKey + `" = true}`,
		"nested key":    `{a = {"` + screenKey + `" = 1}}`,
		"zwsp key":      "{\"\u200b" + screenKey + "\" = \"x\"}",
		"key in array":  `[{"` + screenKey + `" = 1}]`,
	} {
		t.Run(name, func(t *testing.T) {
			assertBothRefuse(t, "plugins.cascade-nself.x", lit)
		})
	}
}

// TestConfigScreenNoFalsePositive: an ordinary dotted host, an ordinary
// inline table and a URL path segment that merely contains "sk-" are
// written by both writers.
func TestConfigScreenNoFalsePositive(t *testing.T) {
	assertBothWrite(t, "plugins.cascade-nself.postgres_host", `"db.internal"`, `"db.internal"`)
	assertBothWrite(t, "plugins.cascade-nself.x", `{host = "db.internal", port = 5432}`, `db.internal`)
	assertBothWrite(t, "plugins.cascade-nself.x", `"/srv/task-runner/desk-1"`, `task-runner`)
}
