// Purpose: manifest codegen — a typed TOML manifest emitted from Go struct
//   literals of the C/S-05.T6 Manifest types (task 2), with no external
//   toolchain: EncodeManifestTOML renders any Manifest to canonical TOML
//   bytes, and GenerateExampleManifest is the author kit's canonical
//   struct-literal example that codegen_test.go's golden test keeps in
//   sync with the checked-in pkg/plugin/testdata/codegen-example.toml
//   fixture via `go generate ./pkg/plugin/...`.
// Inputs: a Manifest value (EncodeManifestTOML); none (GenerateExampleManifest).
// Outputs: TOML bytes that round-trip through ParseManifest and pass
//   Validate with zero findings.
// Constraints: adds no manifest schema types and no validation code (the
//   one-validator invariant — pkg/plugin.Validate from C/S-05.T6 remains
//   the sole validator); no bare fmt.Errorf/errors.New (boundary lint).
// SPORT: pkg/plugin manifest-codegen (ADD) — P1-E15-W4-S33-T1.

//go:generate env CASCADE_GENERATE=1 go test -run TestGenerateManifestGolden -v .

package plugin

import (
	"bytes"
	"io"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EncodeManifest writes m to w as canonical cascade.plugin/v2 TOML, using
// the same encoder family ParseManifest's decoder round-trips against. It
// performs no validation of its own — callers that need m to be a
// well-formed manifest call Validate separately, per the one-validator
// invariant.
func EncodeManifest(w io.Writer, m Manifest) error {
	enc := toml.NewEncoder(w)
	if err := enc.Encode(m); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "plugin codegen: encode manifest TOML")
	}
	return nil
}

// EncodeManifestTOML is EncodeManifest into an in-memory buffer, returning
// the resulting bytes directly — the convenience form codegen_test.go's
// golden test and a plugin author's own tooling call.
func EncodeManifestTOML(m Manifest) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeManifest(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GenerateExampleManifest returns the author kit's canonical example
// Manifest, built entirely from Go struct literals of the C/S-05.T6
// Manifest types — the single source codegen_test.go's golden test encodes
// to TOML and pkg/plugin.Validate checks. A plugin author copies this
// function's body as the starting point for their own manifest.
func GenerateExampleManifest() Manifest {
	return Manifest{
		ID:          "cascade-authorkit-example",
		Name:        "Cascade Author Kit Example",
		Schema:      SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=2.0.0",
		Runtime:     RuntimeWasm,
		Provides: Provides{
			Intents: []IntentSpec{{
				Name:        "authorkit-example.greet",
				Description: "Demonstrate an intent this example plugin can satisfy.",
			}},
			Tools: []ToolSpec{{
				Name:        "authorkit-example.chat",
				Description: "Demonstrate a tool this example plugin exposes.",
			}},
			Domains: []DomainSpec{{
				Name:        "authorkit-example",
				Description: "The example plugin's own namespaced storage domain.",
			}},
			Commands: []CommandSpec{{
				Name:        "authorkit-example",
				Description: "Inspect the example plugin's status.",
				RPCMethod:   "plugin.authorkit-example.status",
			}},
		},
		Requires: []string{"network.egress"},
		Permissions: []PermissionDisplay{{
			Name:        "network.egress",
			Description: "Make outbound network requests on the user's behalf.",
		}},
	}
}
