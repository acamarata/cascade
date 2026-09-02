// Purpose: runnable godoc Examples for this package's two entry points,
// ParseManifest and Validate, per 12-QUALITY-CONSTITUTION.md Art.10.6
// ("Godoc on every exported symbol in pkg/, with a runnable Example where
// the symbol is an entry point"). A sibling ticket already failed review
// for exactly this gap (grep '^func Example' pkg/plugin was empty), so
// these are not optional polish.
// Constraints: Art.2 (real-counterpart verification) requires these parse
// a REAL manifest and show a REAL validation failure, not a self-authored
// toy — ExampleParseManifest reads one of this package's own golden
// testdata fixtures; no writes anywhere (Art.7.1 doesn't apply, this file
// performs no writes).
package plugin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/plugin"
)

// ExampleParseManifest parses one of this package's real golden manifests
// (see testdata/README.md for provenance) and prints its id.
func ExampleParseManifest() {
	data, err := os.ReadFile(filepath.Join("testdata", "example-pbd.toml"))
	if err != nil {
		fmt.Println("read error:", err)
		return
	}

	m, err := plugin.ParseManifest(strings.NewReader(string(data)))
	if err != nil {
		fmt.Println("parse error:", err)
		return
	}

	fmt.Println(m.ID)
	// Output: cascade-pbd
}

// ExampleValidate shows a real validation failure: a manifest whose schema
// field does not equal plugin.SchemaVersion (rule R1) is rejected with an
// ErrCodeSchemaVersion finding.
func ExampleValidate() {
	m := plugin.Manifest{
		ID:          "example-plugin",
		Name:        "Example Plugin",
		Schema:      "cascade.plugin/v1", // wrong on purpose — triggers R1
		Version:     "1.0.0",
		HostVersion: ">=2.0.0",
		Runtime:     plugin.RuntimeBuiltin,
	}

	errs := plugin.Validate(m)
	fmt.Println(errs[0])
	// Output: schema: schema-version: schema must equal "cascade.plugin/v2", got "cascade.plugin/v1"
}
