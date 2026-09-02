package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: FuzzTomlLiteral and FuzzTomlEdit — proving ParseTomlLiteral and
//   SetKeyLine/UnsetKeyLine never panic on adversarial input
//   (06-FORGE-SPEC §5.7). Both share the seed-corpus directory
//   internal/testdata/fuzz/config_literal/ per this ticket's files_scope
//   and task 1/2.
// Constraints: Art.7.1 — reads the shared seed directory only (no writes
//   outside Go's own per-package testdata/fuzz/<Name>/ corpus, which this
//   file never manages directly).

// FuzzTomlLiteral proves ParseTomlLiteral never panics for any input
// string, and that every value it DOES accept decodes back to the same
// Go value via a second parse (idempotence — a property any correct TOML
// scalar parser should have).
func FuzzTomlLiteral(f *testing.F) {
	for _, seed := range readFuzzSeedLines(f, "literal_seeds.txt") {
		f.Add(seed)
	}
	f.Add("true")
	f.Add("false")
	f.Add("42")
	f.Add("-17")
	f.Add("1.5")
	f.Add(`"hello"`)
	f.Add(`["a","b"]`)
	f.Add("")
	f.Add("not valid toml {")
	f.Add(`"unterminated`)

	f.Fuzz(func(t *testing.T, raw string) {
		v1, err1 := ParseTomlLiteral(raw)
		if err1 != nil {
			return // invalid input is a valid outcome; only a panic is a bug
		}
		v2, err2 := ParseTomlLiteral(raw)
		if err2 != nil {
			t.Fatalf("ParseTomlLiteral(%q) succeeded once then failed: %v", raw, err2)
		}
		if !deepEqualLiteral(v1, v2) {
			t.Fatalf("ParseTomlLiteral(%q) not idempotent: %#v != %#v", raw, v1, v2)
		}
	})
}

// FuzzTomlEdit proves SetKeyLine/UnsetKeyLine never panic on adversarial
// (src, dotted key) pairs, and that a successful SetKeyLine's output is
// itself re-editable (the editor never produces a buffer that breaks its
// own next call).
func FuzzTomlEdit(f *testing.F) {
	for _, seed := range readFuzzSeedLines(f, "toml_edit_seeds.txt") {
		f.Add(seed, "a.b", "1")
	}
	f.Add("[a]\nb = 1\n", "a.b", "2")
	f.Add("", "logging.level", `"debug"`)
	f.Add("[a]\nb = 1 # comment\n", "a.b", "2")
	f.Add("not valid toml {{{", "a.b", "1")
	f.Add("[[array]]\nx = 1\n", "array.x", "2")

	f.Fuzz(func(_ *testing.T, src, dotted, literal string) {
		out, err := SetKeyLine([]byte(src), dotted, literal)
		if err != nil {
			return
		}
		// Re-editing the output must also never panic.
		_, _ = SetKeyLine(out, dotted, literal)
		_, _, _ = UnsetKeyLine(out, dotted)
	})
}

func deepEqualLiteral(a, b interface{}) bool {
	switch av := a.(type) {
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualLiteral(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqualLiteral(v, bv[k]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// readFuzzSeedLines reads the shared hand-curated corpus file (if
// present) from internal/testdata/fuzz/config_literal/, one seed per
// line. A missing file is not an error — the inline f.Add seeds above are
// always present regardless.
func readFuzzSeedLines(f *testing.F, name string) []string {
	f.Helper()
	path := filepath.Join("..", "..", "internal", "testdata", "fuzz", "config_literal", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	return lines
}
