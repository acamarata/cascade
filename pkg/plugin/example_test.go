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
	"context"
	"encoding/json"
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

// ExampleGenerateExampleManifest builds the author kit's canonical example
// manifest from Go struct literals and encodes it to TOML — the codegen
// entry point pkg/plugin/testdata/codegen-example.toml is generated from.
func ExampleGenerateExampleManifest() {
	m := plugin.GenerateExampleManifest()

	data, err := plugin.EncodeManifestTOML(m)
	if err != nil {
		fmt.Println("encode error:", err)
		return
	}

	// Round-trip through the same parser real manifests go through, to
	// show the codegen output is a genuine cascade.plugin/v2 document.
	parsed, err := plugin.ParseManifest(strings.NewReader(string(data)))
	if err != nil {
		fmt.Println("parse error:", err)
		return
	}
	fmt.Println(parsed.ID)
	// Output: cascade-authorkit-example
}

// ExampleGuestDispatcher shows a guest binary registering an
// AgentProviderMethod handler and routing a plugin_invoke envelope to it —
// the pattern a wasm guest's real plugin_invoke export follows per
// R-14.50.
func ExampleGuestDispatcher() {
	d := plugin.NewGuestDispatcher()
	d.Register(plugin.MethodChat, func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"reply":"hello"}`), nil
	})

	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: plugin.MethodChat.String(), Params: json.RawMessage(`{}`)})
	if err != nil {
		fmt.Println("marshal error:", err)
		return
	}

	out := d.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		fmt.Println("unmarshal error:", err)
		return
	}
	fmt.Println(string(res.Result))
	// Output: {"reply":"hello"}
}
