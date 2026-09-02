// Purpose: shared test fixtures/helpers plus the happy-path golden
// round-trip test for Manifest/ParseManifest/Validate. Split from a single
// manifest_test.go per R-14.117 (Art.10.3's 300-line file cap): this file
// keeps the shared substrate (goldenFixtures, readTestdata,
// baseManifestTOML, errAs, validManifest) plus TestParseManifest_Goldens;
// the rejection rules move to validate_rules_test.go and the
// loader/errcode tests move to loader_test.go. id_validation_test.go and
// host_version_test.go (CR remediation, R-14.127/R-14.128) reuse
// validManifest as their shared substrate. All relocations from the
// pre-split file are behaviour-preserving — no assertion, name, or
// signature changed.
// Constraints: no network calls (Art.7.2); no writes outside t.TempDir()
// (Art.7.1 — this file performs no writes at all, only reads of testdata/).
package plugin_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/plugin"
)

// goldenFixtures lists the three golden manifest files this ticket ships,
// per 02-TARGET-STRUCTURE.md §First-party plugin catalog v1 (see
// pkg/plugin/testdata/README.md for provenance).
var goldenFixtures = []string{
	"example-connector.toml",
	"example-pbd.toml",
	"example-agent-provider.toml",
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	return data
}

// TestParseManifest_Goldens covers the happy-path parse+validate round-trip
// for all three golden fixtures, plus the "remarshal produces an identical
// Manifest" acceptance criterion.
func TestParseManifest_Goldens(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			data := readTestdata(t, name)

			m, err := plugin.ParseManifest(strings.NewReader(string(data)))
			if err != nil {
				t.Fatalf("ParseManifest(%s) = _, %v, want nil error", name, err)
			}
			if m.Schema != plugin.SchemaVersion {
				t.Errorf("%s: Schema = %q, want %q", name, m.Schema, plugin.SchemaVersion)
			}
			if errs := plugin.Validate(m); len(errs) != 0 {
				t.Errorf("%s: Validate(parsed manifest) = %v, want no errors", name, errs)
			}

			// Remarshal round-trip: encode the parsed Manifest back to TOML
			// and reparse it; the result must be identical to the original.
			remarshaled, err := toml.Marshal(m)
			if err != nil {
				t.Fatalf("%s: toml.Marshal(m): %v", name, err)
			}
			m2, err := plugin.ParseManifest(strings.NewReader(string(remarshaled)))
			if err != nil {
				t.Fatalf("%s: ParseManifest(remarshal): %v", name, err)
			}
			if !reflect.DeepEqual(m, m2) {
				t.Errorf("%s: round-trip mismatch:\n  original:  %+v\n  remarshal: %+v", name, m, m2)
			}
		})
	}
}

// validManifest returns a Manifest that passes Validate with zero findings,
// for tests that only want to vary one field via direct struct construction
// (id_validation_test.go, host_version_test.go) rather than round-tripping
// through TOML text. Each caller mutates a copy — this returns by value.
func validManifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "valid-plugin-id",
		Name:        "Valid Plugin",
		Schema:      plugin.SchemaVersion,
		Version:     "1.0.0",
		HostVersion: ">=2.0.0",
		Runtime:     plugin.RuntimeBuiltin,
		Requires:    []string{"storage.domain"},
	}
}

// baseManifestTOML is a minimal, otherwise-valid cascade.plugin/v2 document
// used as the substrate for the rejection-rule test table in
// validate_rules_test.go: each case overrides exactly the field(s) needed
// to trigger one rule, isolating it from the other five.
const baseManifestTOML = `
id = "test-plugin"
name = "Test Plugin"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "builtin"
requires = ["storage.domain"]
`

// errAs is a small errors.As wrapper kept local to this package's tests so
// the rejection-rule table in validate_rules_test.go reads without
// repeating the two-line pattern at every call site.
func errAs(err error, target *plugin.ValidationError) bool {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if ve, ok := err.(plugin.ValidationError); ok {
			*target = ve
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
