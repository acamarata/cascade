// Command example-domain is the first-party example of the manifest v2
// SDK's most demanding pattern: a raw→canonical→derived pipeline plus
// per-connector permission declarations (02-TARGET-STRUCTURE.md §First-party
// plugin catalog v1: examples/example-domain, wasm, off).
//
// Purpose: the torture-test's domain leg (O/S-33.T2) — every stage of the
//
//	pipeline is real, deterministic logic with a typed, fail-closed error
//	path; no stage is a stub or a mock return.
//
// Inputs: rawRecord values shaped identically to example-connector's own
//
//	rawRecord (the sibling package's connector output).
//
// Outputs: canonicalRecord and derivedRecord values, or a typed
//
//	*cascade.Error (KindInvalidInput) at whichever stage first finds
//	malformed input.
//
// Constraints: imports pkg/** only, never internal/** (Art.10.2); must
//
//	compile under GOOS=wasip1 GOARCH=wasm; no bare fmt.Errorf/errors.New
//	(boundary lint); catalogued "off" so main is intentionally inert — see
//	main.go.
//
// SPORT: plugins/examples/example-domain (ADD) — P1-E15-W4-S33-T2.
package main

import (
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rawRecord mirrors example-connector's own rawRecord shape exactly: this
// plugin's raw layer is what a connector plugin like example-connector
// produces.
type rawRecord struct {
	ID             string
	Source         string
	Payload        map[string]string
	CapturedAtUnix int64
}

// canonicalRecord is raw normalized into the domain's own vocabulary: every
// payload key lowercased and trimmed, nothing dropped.
type canonicalRecord struct {
	ID               string
	Source           string
	Fields           map[string]string
	NormalizedAtUnix int64
}

// derivedRecord is a deterministic summary computed from one
// canonicalRecord: never AI-generated, never a stub — a plain, reproducible
// aggregate over Fields.
type derivedRecord struct {
	ID              string
	FieldCount      int
	FieldSummary    string
	DerivedFromUnix int64
}

// toCanonical implements the raw→canonical stage. It fails closed
// (cascade.KindInvalidInput) on an empty id, an empty source, or a nil
// payload — never silently substituting a default.
func toCanonical(raw rawRecord) (canonicalRecord, error) {
	if strings.TrimSpace(raw.ID) == "" {
		return canonicalRecord{}, cascade.New(cascade.KindInvalidInput, "example-domain: raw record id must not be empty")
	}
	if strings.TrimSpace(raw.Source) == "" {
		return canonicalRecord{}, cascade.New(cascade.KindInvalidInput, "example-domain: raw record source must not be empty")
	}
	if raw.Payload == nil {
		return canonicalRecord{}, cascade.New(cascade.KindInvalidInput, "example-domain: raw record payload must not be nil")
	}
	fields := make(map[string]string, len(raw.Payload))
	for k, v := range raw.Payload {
		fields[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return canonicalRecord{
		ID:               raw.ID,
		Source:           raw.Source,
		Fields:           fields,
		NormalizedAtUnix: raw.CapturedAtUnix,
	}, nil
}

// toDerived implements the canonical→derived stage. It fails closed on an
// empty id or a canonical record with no fields to derive a summary from.
func toDerived(c canonicalRecord) (derivedRecord, error) {
	if strings.TrimSpace(c.ID) == "" {
		return derivedRecord{}, cascade.New(cascade.KindInvalidInput, "example-domain: canonical record id must not be empty")
	}
	if len(c.Fields) == 0 {
		return derivedRecord{}, cascade.New(cascade.KindInvalidInput, "example-domain: canonical record has no fields to derive from")
	}
	keys := make([]string, 0, len(c.Fields))
	for k := range c.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return derivedRecord{
		ID:              c.ID,
		FieldCount:      len(c.Fields),
		FieldSummary:    strings.Join(keys, ","),
		DerivedFromUnix: c.NormalizedAtUnix,
	}, nil
}

// runPipeline chains toCanonical and toDerived: the full raw→canonical→
// derived path this ticket's AC requires end-to-end, with no stage skipped
// or stubbed.
func runPipeline(raw rawRecord) (derivedRecord, error) {
	canonical, err := toCanonical(raw)
	if err != nil {
		return derivedRecord{}, err
	}
	return toDerived(canonical)
}
