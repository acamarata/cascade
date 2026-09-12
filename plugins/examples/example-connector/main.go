// Command example-connector is the first-party example of a minimal but
// complete connector-shaped WASM plugin (02-TARGET-STRUCTURE.md §First-party
// plugin catalog v1: examples/example-connector, wasm, off).
//
// Purpose: prove the manifest v2 SDK under a connector's real shape (raw
//
//	storage domain, network + storage permission declarations) as one leg of
//	O/S-33.T2's torture test; example-domain (the sibling package) consumes
//	the same rawRecord shape as its pipeline's input.
//
// Inputs: none at process start; connector.Fetch takes no arguments and
//
//	returns a deterministic batch of records from the connector's own
//	declared source name.
//
// Outputs: []rawRecord from connector.Fetch, or a typed *cascade.Error
//
//	(KindInvalidInput) when the connector was constructed with an empty
//	source name.
//
// Constraints: imports pkg/** only, never internal/** (Art.10.2); must
//
//	compile under GOOS=wasip1 GOARCH=wasm (this ticket's AC); no bare
//	fmt.Errorf/errors.New (boundary lint); this plugin is catalogued "off"
//	(02-TARGET-STRUCTURE) so main is intentionally inert — the real,
//	tested logic is connector.Describe/Fetch below, exercised by
//	connector_test.go and by plugins/examples/integration_test.go's manifest
//	+ WASM-compile checks.
//
// SPORT: plugins/examples/example-connector (ADD) — P1-E15-W4-S33-T2.
package main

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rawRecord is one unit of data a connector fetches from its upstream
// source, before any normalization. example-domain's pipeline (domain.go,
// the sibling package) accepts the same shape as its raw input.
type rawRecord struct {
	ID             string
	Source         string
	Payload        map[string]string
	CapturedAtUnix int64
}

// connector is the minimal but complete contract a data-source plugin
// implements: it names itself and fetches its current batch of raw
// records. This is intentionally small — real connector implementations
// (Google/Apple/wearable-style sources, per the torture-test's source
// material) all reduce to this same two-method shape at the plugin
// boundary; anything source-specific belongs behind Fetch, never in the
// contract itself.
type connector interface {
	// Describe returns a human-readable name for this connector's source.
	Describe() string
	// Fetch returns the connector's current batch of raw records, or a
	// typed error if the connector cannot produce one.
	Fetch() ([]rawRecord, error)
}

// var _ connector = exampleConnector{} proves exampleConnector actually
// satisfies connector at compile time — a torture-test fixture with a
// contract nothing verifies is not a fixture.
var _ connector = exampleConnector{}

// exampleConnector is a real, deterministic connector implementation: no
// network I/O (a WASM guest under this ticket's AC has no host_http grant
// declared), just a fixed, reproducible batch keyed off its own source
// name.
type exampleConnector struct {
	source string
}

// Describe implements connector.
func (c exampleConnector) Describe() string { return c.source }

// Fetch implements connector. It refuses a connector built with an empty
// source name (fail-closed, never a silent empty batch) and otherwise
// returns two real records deterministically derived from the source name.
func (c exampleConnector) Fetch() ([]rawRecord, error) {
	trimmed := strings.TrimSpace(c.source)
	if trimmed == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "example-connector: source must not be empty")
	}
	return []rawRecord{
		{
			ID:             trimmed + "-1",
			Source:         trimmed,
			Payload:        map[string]string{"kind": "sample", "seq": "1"},
			CapturedAtUnix: 1,
		},
		{
			ID:             trimmed + "-2",
			Source:         trimmed,
			Payload:        map[string]string{"kind": "sample", "seq": "2"},
			CapturedAtUnix: 2,
		},
	}, nil
}

// main is intentionally inert: this plugin is catalogued "off"
// (02-TARGET-STRUCTURE.md §First-party plugin catalog v1), so no host-ABI
// wiring runs at process start. The real contract is connector, verified by
// connector_test.go and by the manifest/WASM-compile checks in
// integration_test.go.
func main() {}
