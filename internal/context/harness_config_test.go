package context

// Purpose: the harness state-file parser's tests, including the Art.2
//   real-counterpart capture and its fuzz target. Split from
//   harness_test.go to stay under Art.10.3's 300-line cap.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHarnessRealCounterpartFixture is the Art.2 assertion: the parser
// reads a state file a real harness really wrote. Its provenance, and
// exactly which three values were redacted, are in testdata/README.md.
//
// The capture's value is the two-scope MCP table — a user-scoped
// mcpServers at the root and a project-scoped one nested under
// projects.<path> — which is the shape the parser must handle and which
// no reconstruction from memory would have produced.
func TestHarnessRealCounterpartFixture(t *testing.T) {
	raw := realHarnessConfig(t)

	cfg, err := ParseHarnessConfig(raw)
	if err != nil {
		t.Fatalf("the parser refused a config a real harness wrote: %v", err)
	}
	if cfg.Version == "" {
		t.Error("no version was read from a file that records one")
	}
	if !cfg.CascadeRegistered {
		t.Error("cascade is registered in the capture and the parser did not see it")
	}

	var everyKey map[string]any
	if err := json.Unmarshal(raw, &everyKey); err != nil {
		t.Fatalf("the fixture is not valid JSON: %v", err)
	}
	if _, ok := everyKey["projects"]; !ok {
		t.Error("the fixture has no per-project nesting; this is the shape of a reconstruction, not a capture")
	}
	if _, ok := everyKey["mcpServers"]; !ok {
		t.Error("the fixture has no root-scoped mcpServers; the two-scope shape is the reason it was captured")
	}
}

// TestCascadeIsFoundAtEitherScope pins both halves of that shape
// independently, so a parser that happened to find cascade at one scope
// cannot pass for one that reads both.
func TestCascadeIsFoundAtEitherScope(t *testing.T) {
	for name, raw := range map[string]string{
		"root scope":    `{"mcpServers":{"cascade":{}}}`,
		"project scope": `{"projects":{"/p":{"mcpServers":{"cascade":{}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseHarnessConfig([]byte(raw))
			if err != nil {
				t.Fatalf("ParseHarnessConfig: %v", err)
			}
			if !cfg.CascadeRegistered {
				t.Fatal("cascade was registered and the parser did not see it")
			}
		})
	}
	cfg, err := ParseHarnessConfig([]byte(`{"mcpServers":{"other":{}},"projects":{"/p":{"mcpServers":{"another":{}}}}}`))
	if err != nil {
		t.Fatalf("ParseHarnessConfig: %v", err)
	}
	if cfg.CascadeRegistered {
		t.Fatal("cascade was reported registered in a config that does not mention it")
	}
}

// TestAnEmptyConfigIsNotAnError pins the fresh-install case. A harness
// that has created its file but not written to it is normal, and
// refusing it would make a fresh install look like a corrupt one.
func TestAnEmptyConfigIsNotAnError(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t "} {
		cfg, err := ParseHarnessConfig([]byte(raw))
		if err != nil {
			t.Fatalf("an empty config was refused: %v", err)
		}
		if cfg != (HarnessConfig{}) {
			t.Fatalf("an empty config produced %+v", cfg)
		}
	}
}

// TestAMalformedConfigIsRefused covers the other direction: content that
// is present and unreadable is an error, not an empty answer.
func TestAMalformedConfigIsRefused(t *testing.T) {
	for _, raw := range []string{"{{{", "not json at all", `{"mcpServers":`} {
		if _, err := ParseHarnessConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed config %q", raw)
		}
	}
}

// TestUnknownFieldsAreIgnored proves the parser tolerates a newer
// harness. Refusing an unknown field would report a machine as
// unreadable the day its harness updated.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	cfg, err := ParseHarnessConfig([]byte(`{"version":"9.9","aFieldFromNextYear":{"deeply":["nested"]},"mcpServers":{"cascade":{}}}`))
	if err != nil {
		t.Fatalf("a config with one unknown field was refused: %v", err)
	}
	if cfg.Version != "9.9" || !cfg.CascadeRegistered {
		t.Fatalf("got %+v", cfg)
	}
}

// realHarnessConfig reads the captured fixture.
func realHarnessConfig(t testing.TB) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cc-harness-fixtures", "harness-config.json"))
	if err != nil {
		t.Fatalf("read the captured harness config: %v", err)
	}
	return raw
}

// FuzzHarnessConfigParse proves the parser never panics on bytes this
// program did not write.
func FuzzHarnessConfigParse(f *testing.F) {
	f.Add(string(realHarnessConfig(f)))
	f.Add("")
	f.Add("null")
	f.Add("[]")
	f.Add(`{"mcpServers":"not an object"}`)
	f.Add(`{"projects":{"/p":"not an object"}}`)
	f.Add(`{"version":123}`)
	f.Fuzz(func(t *testing.T, raw string) {
		cfg, err := ParseHarnessConfig([]byte(raw))
		if err != nil && cfg != (HarnessConfig{}) {
			t.Fatalf("a refusal still returned %+v for %q", cfg, raw)
		}
	})
}

// TestDetectReadsTheHarnessConfig proves the parser is actually wired
// into detection, over the real captured fixture.
//
// Without it ParseHarnessConfig was built, tested and unreachable — which
// is exactly what the test-only gate caught when this ticket first
// shipped the parser and the detector separately.
func TestDetectReadsTheHarnessConfig(t *testing.T) {
	captured := realHarnessConfig(t)
	home := "/h"
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	detector := NewPathDetector("darwin", env, func(string) bool { return true })
	root, err := detector.configRoot(HarnessClaude)
	if err != nil {
		t.Fatalf("configRoot: %v", err)
	}
	wantPath := filepath.Join(root, ".claude.json")

	var asked []string
	states, err := detector.WithFileReader(func(path string) ([]byte, error) {
		asked = append(asked, path)
		if path == wantPath {
			return captured, nil
		}
		return nil, os.ErrNotExist
	}).Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(asked) == 0 {
		t.Fatal("detection read no config file; the parser is not wired in")
	}

	byKind := map[HarnessKind]HarnessState{}
	for _, s := range states {
		byKind[s.Kind] = s
	}
	claude := byKind[HarnessClaude]
	if claude.Version == "" || !claude.CascadeRegistered {
		t.Fatalf("the captured config was read as %+v", claude)
	}
	// codex keeps a TOML config this parser does not read, so its columns
	// are absent by design rather than by accident.
	if codex := byKind[HarnessCodex]; codex.Version != "" || codex.CascadeRegistered {
		t.Errorf("codex reported config data from a format this build does not read: %+v", codex)
	}
}

// TestDetectSurvivesAnUnreadableConfig pins the best-effort rule: losing
// the version columns is acceptable, losing "this harness is installed"
// is not.
func TestDetectSurvivesAnUnreadableConfig(t *testing.T) {
	env := func(key string) string {
		if key == "HOME" {
			return "/h"
		}
		return ""
	}
	for name, read := range map[string]FileReader{
		"unreadable": func(string) ([]byte, error) { return nil, os.ErrPermission },
		"malformed":  func(string) ([]byte, error) { return []byte("{{{"), nil },
		"absent":     func(string) ([]byte, error) { return nil, os.ErrNotExist },
	} {
		t.Run(name, func(t *testing.T) {
			states, err := NewPathDetector("darwin", env, func(string) bool { return true }).
				WithFileReader(read).Detect(context.Background())
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			for _, s := range states {
				if !s.Detected {
					t.Errorf("%q lost its detection to an unreadable config", s.Kind)
				}
				if s.Version != "" || s.CascadeRegistered {
					t.Errorf("%q reported config data nothing could read: %+v", s.Kind, s)
				}
			}
		})
	}
}

// TestDetectWithoutAReaderOpensNothing pins the optional half: a caller
// asking only what is installed opens no file at all.
func TestDetectWithoutAReaderOpensNothing(t *testing.T) {
	env := func(key string) string {
		if key == "HOME" {
			return "/h"
		}
		return ""
	}
	states, err := NewPathDetector("darwin", env, func(string) bool { return true }).Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	for _, s := range states {
		if !s.Detected {
			t.Errorf("%q was not detected", s.Kind)
		}
		if s.Version != "" || s.CascadeRegistered {
			t.Errorf("%q reported config data with no reader wired: %+v", s.Kind, s)
		}
	}
}
