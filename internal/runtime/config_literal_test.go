package runtime

// Purpose: tests for canonicalLiteral (config_literal.go), Set's canonical
//   write, and the [plugins.<name>] size bound (config_plugins.go).
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: each expected encoding is written out by hand, never
//   derived from the encoder under test.
// SPORT: internal/runtime config_literal.go (TEST) — P1-E25-W5-S103-T1.

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCanonicalLiteralReencodes(t *testing.T) {
	cases := map[string]string{
		`'single'`:                `"single"`,
		`"a\u0001b"`:              `"a\u0001b"`,
		`"line\nbreak"`:           `"line\nbreak"`,
		`2.0`:                     `2.0`,
		`1e3`:                     `1000.0`,
		`42  # trailing comment`:  `42`,
		`[ 1,2 ,3 ]`:              `[1, 2, 3]`,
		`{ z = 1, a = "x" }`:      `{a = "x", z = 1}`,
		`{"a.b" = true}`:          `{"a.b" = true}`,
		"\"\"\"multi\nline\"\"\"": `"multi\nline"`,
	}
	for in, want := range cases {
		_, got, err := canonicalLiteral(in)
		if err != nil {
			t.Errorf("canonicalLiteral(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("canonicalLiteral(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("canonicalLiteral(%q) = %q carries a raw line break", in, got)
		}
	}
}

// TestCanonicalLiteralRefusesUnstableReencoding exercises canonicalLiteral's
// back-validation (config_literal.go: the ParseTomlLiteral(canonical) call
// after encodeTomlValue, guarding against a re-encoding that does not
// survive a round trip) directly, with an input ParseTomlLiteral itself
// accepts outright. "nan" is a valid TOML float literal: go-toml decodes
// "v = nan" to a lone float64 math.NaN() and ParseTomlLiteral's own
// len(holder) != 1 gate (config_write.go) does not reject it, since holder
// has exactly the one key "v". canonicalLiteral then re-encodes that value
// to the string "nan" (canonicalFloat) and re-parses it to prove the round
// trip — but reflect.DeepEqual(NaN, NaN) is false (DeepEqual uses == for
// float64, and NaN != NaN under ==), so the back-validation refuses it even
// though "nan" reads back as the same *kind* of value. This is a real,
// reachable case distinct from ParseTomlLiteral's own front-end check.
func TestCanonicalLiteralRefusesUnstableReencoding(t *testing.T) {
	for _, raw := range []string{"nan", "+nan", "-nan"} {
		if _, err := ParseTomlLiteral(raw); err != nil {
			t.Fatalf("ParseTomlLiteral(%q) = %v, want it accepted (this test needs an input the front-end passes)", raw, err)
		}
		_, _, err := canonicalLiteral(raw)
		if err == nil {
			t.Fatalf("canonicalLiteral(%q) = nil error, want the back-validation to refuse a NaN re-encoding that cannot round-trip under reflect.DeepEqual", raw)
		}
		var lerr *LiteralError
		if !errors.As(err, &lerr) {
			t.Fatalf("canonicalLiteral(%q) err = %v (%T), want *LiteralError", raw, err, err)
		}
		if !strings.Contains(lerr.Hint, "does not survive a canonical re-encoding") {
			t.Fatalf("canonicalLiteral(%q) hint = %q, want it to name the re-encoding refusal", raw, lerr.Hint)
		}
	}
}

func TestConfigSetWritesCanonicalLiteral(t *testing.T) {
	w := writerAt(t, "")
	if _, err := w.Set("runtime.profile", `'server'  # note`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	data, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "[runtime]\nprofile = \"server\"\n" {
		t.Fatalf("Set wrote the caller's text instead of the canonical value:\n%s", data)
	}
}

func TestParsePluginsSectionBoundsOpaqueTable(t *testing.T) {
	small := map[string]interface{}{"plugins": map[string]interface{}{
		"cascade-nself": map[string]interface{}{"x": strings.Repeat("a", 1024)},
	}}
	if _, err := parsePluginsSection(small); err != nil {
		t.Fatalf("a 1 KiB plugin table was refused: %v", err)
	}
	big := map[string]interface{}{"plugins": map[string]interface{}{
		"cascade-nself": map[string]interface{}{"x": strings.Repeat("a", 64<<10)},
	}}
	_, err := parsePluginsSection(big)
	var cerr *ConfigError
	if !errors.As(err, &cerr) || cerr.Field != "plugins.cascade-nself" {
		t.Fatalf("parsePluginsSection(64 KiB table) err = %v, want *ConfigError on plugins.cascade-nself", err)
	}

	w := writerAt(t, "[plugins.cascade-nself]\nx = \""+strings.Repeat("a", 64<<10-32)+"\"\n")
	before, _ := os.ReadFile(w.Path)
	if _, err := w.Set("plugins.cascade-nself.y", `"`+strings.Repeat("b ", 40)+`"`); !errors.As(err, &cerr) {
		t.Fatalf("Set past the bound err = %v, want *ConfigError", err)
	}
	after, _ := os.ReadFile(w.Path)
	if string(before) != string(after) {
		t.Fatal("a Set refused by the bound still wrote the file")
	}
}

// TestScreenConfigLiteralAgreesWithWriters: the screen a proposing plugin
// calls refuses exactly what Set refuses (one validator, vetLiteral): a
// split secret, a userinfo URL and an injected table, and accepts a host.
func TestScreenConfigLiteralAgreesWithWriters(t *testing.T) {
	const path = "plugins.cascade-nself.postgres_host"
	var secret *SecretLiteralError
	for name, value := range splitSecretValues {
		if err := ScreenConfigLiteral(path, tomlBasicString(value)); !errors.As(err, &secret) {
			t.Errorf("%s: ScreenConfigLiteral = %v, want *SecretLiteralError", name, err)
		}
	}
	if err := ScreenConfigLiteral(path, `"postgres://admin:hunter2@db"`); !errors.As(err, &secret) {
		t.Errorf("userinfo URL: ScreenConfigLiteral = %v, want *SecretLiteralError", err)
	}
	var lit *LiteralError
	if err := ScreenConfigLiteral(path, injectionPayloads["agents.egress"]); !errors.As(err, &lit) {
		t.Errorf("table injection: ScreenConfigLiteral = %v, want *LiteralError", err)
	}
	if err := ScreenConfigLiteral(path, `"db.internal"`); err != nil {
		t.Errorf("plain host refused: %v", err)
	}
}
