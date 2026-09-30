package runtime

// Purpose: regression tests for a secret split across whitespace (the
//   P1-PLG-01 adversarial-review case): Set and ApplyDiff share one value
//   validator (vetLiteral) and both refuse a known-prefix secret whether a
//   space, a tab or several spaces cut it, or whitespace sits before it,
//   while ordinary values with or without spaces are still written.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: credential-shaped literals are built by concatenation
//   (C22); every refusal asserts the file is byte-identical afterwards.
// SPORT: internal/runtime config_diff.go (TEST) — P1-PLG-01.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// splitSecretHead and splitSecretTail are the two halves of a known-prefix
// key; joined they are the shape LooksLikeSecret refuses.
const (
	splitSecretHead = "sk-" + "live-" + "abcdefghijkl"
	splitSecretTail = "mnopqrstuvwx"
)

// splitSecretValues are the whitespace-split shapes both writers refuse.
var splitSecretValues = map[string]string{
	"one space":          splitSecretHead + " " + splitSecretTail,
	"tab":                splitSecretHead + "\t" + splitSecretTail,
	"several spaces":     splitSecretHead + "   " + splitSecretTail,
	"leading space":      " " + splitSecretHead + splitSecretTail,
	"leading tab":        "\t" + splitSecretHead + splitSecretTail,
	"after a word":       "host " + splitSecretHead + splitSecretTail,
	"prefix cut in two":  "s k-" + "live-" + "abcdefghijkl" + splitSecretTail,
	"github prefix, tab": "note\t" + "gh" + "p_" + strings.Repeat("A", 36),
}

func TestConfigRefusesWhitespaceSplitSecret(t *testing.T) {
	for name, value := range splitSecretValues {
		t.Run(name, func(t *testing.T) {
			lit := tomlBasicString(value)
			w := writerAt(t, guardSeed)
			_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
				{Path: "plugins.cascade-nself.project_dir", Literal: `"/p"`},
				{Path: "plugins.cascade-nself.postgres_host", Literal: lit},
			}})
			assertKind(t, err, cascade.KindPolicyDenied)
			var secret *SecretLiteralError
			if !errors.As(err, &secret) {
				t.Fatalf("ApplyDiff(%q) err = %v, want *SecretLiteralError", value, err)
			}
			assertUnchanged(t, w)
			_, err = w.Set("plugins.cascade-nself.postgres_host", lit)
			if !errors.As(err, &secret) {
				t.Fatalf("Set(%q) err = %v, want *SecretLiteralError", value, err)
			}
			assertUnchanged(t, w)
		})
	}
}

// TestConfigWritesOrdinaryValuesWithSpaces is the no-false-positive leg:
// a plain host, a path holding spaces (long enough that a joined
// bare-base64 check would have refused it) and prose are all written.
func TestConfigWritesOrdinaryValuesWithSpaces(t *testing.T) {
	values := map[string]string{
		"postgres_host": "db.internal",
		"project_dir":   "/Users/someone/My Projects/nself fixture/CascadeServerProfile",
		"note":          "primary database for the staging stack",
	}
	w := writerAt(t, guardSeed)
	var entries []DiffEntry
	for key, v := range values {
		entries = append(entries, DiffEntry{Path: "plugins.cascade-nself." + key, Literal: tomlBasicString(v)})
	}
	res, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: entries})
	if err != nil {
		t.Fatalf("ApplyDiff(ordinary values): %v", err)
	}
	if len(res.Applied) != len(values) {
		t.Fatalf("applied = %+v, want all %d entries", res.Applied, len(values))
	}
	data, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, v := range values {
		if !strings.Contains(string(data), v) {
			t.Fatalf("config.toml lacks %q:\n%s", v, data)
		}
	}
	if _, err := w.Set("plugins.cascade-nself.label", `"server local"`); err != nil {
		t.Fatalf("Set(prose with a space): %v", err)
	}
}
