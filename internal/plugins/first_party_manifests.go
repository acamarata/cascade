// Package plugins (first_party_manifests.go): Purpose: the embedded copies
// of the first-party process plugins' manifests, the only manifests whose
// tools may be offered for MCP exposure.
// Inputs: firstparty/github.manifest.toml, a byte-identical copy of
// plugins/github/manifest.toml (TestFirstPartyManifestMatchesShipped).
// Outputs: FirstPartyManifests, each entry parsed and validated.
// Constraints: nothing is read from disk at run time; a manifest that fails
// to parse or validate contributes nothing.
// SPORT: internal/plugins first-party manifests (P1-PLG-06).
package plugins

import (
	"bytes"
	_ "embed" // the first-party manifest copies are embedded

	"github.com/acamarata/cascade/pkg/plugin"
)

//go:embed firstparty/github.manifest.toml
var githubManifestBytes []byte

// firstPartyManifestBytes is every embedded first-party manifest. Tests
// replace it to drive the parse and validate failure paths.
var firstPartyManifestBytes = [][]byte{githubManifestBytes}

// Validated is one embedded first-party manifest that parsed and passed
// plugin.Validate, with the exact bytes it was parsed from.
type Validated struct {
	Manifest plugin.Manifest
	Raw      []byte
}

// FirstPartyManifests returns every embedded first-party manifest that
// parses and validates, in embedding order. A failing manifest is skipped.
func FirstPartyManifests() []Validated {
	out := make([]Validated, 0, len(firstPartyManifestBytes))
	for _, raw := range firstPartyManifestBytes {
		m, err := plugin.ParseManifest(bytes.NewReader(raw))
		if err != nil || len(plugin.Validate(m)) > 0 {
			continue
		}
		out = append(out, Validated{Manifest: m, Raw: append([]byte(nil), raw...)})
	}
	return out
}
