package provider

// Purpose: CapabilityState's wire encoding (P1-E16-W4-S35-T10) — the name
//   it writes, and the ordinal earlier builds wrote, which the provider
//   registry still has on disk.
// Constraints: this value is PERSISTED, not only displayed. The registry
//   stores each record's capabilities as JSON in sqlite, so a build that
//   only read names would make every row an existing installation already
//   has undecodable. That is not hypothetical: it happened, and `provider
//   list` failed outright against a registry populated minutes earlier.
// SPORT: pkg.provider capability state encoding (ADD) —
//   P1-E16-W4-S35-T10.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestACapabilityStateIsWrittenAsItsName: the ordinal says nothing on its
// own and changes meaning if a member is ever inserted.
func TestACapabilityStateIsWrittenAsItsName(t *testing.T) {
	for state, want := range map[CapabilityState]string{
		CapabilityUnknown:     `"unknown"`,
		CapabilitySupported:   `"supported"`,
		CapabilityUnsupported: `"unsupported"`,
	} {
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshalling %v: %v", state, err)
		}
		if string(raw) != want {
			t.Errorf("marshal(%v) = %s, want %s", state, raw, want)
		}
	}
}

// TestALegacyOrdinalStillDecodes is the compatibility half, and the one
// that matters to somebody upgrading.
//
// Reads accept the old encoding, writes emit only the new one, so the
// ordinals drain out of the store as records are re-verified and nothing
// has to migrate. Asserted per member rather than for one value: an
// off-by-one in the ordinal branch would silently reclassify every
// "supported" row as "unknown", which is the direction that loses
// information without looking like a failure.
func TestALegacyOrdinalStillDecodes(t *testing.T) {
	for raw, want := range map[string]CapabilityState{
		"0": CapabilityUnknown,
		"1": CapabilitySupported,
		"2": CapabilityUnsupported,
	} {
		var got CapabilityState
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("decoding the legacy ordinal %s: %v", raw, err)
		}
		if got != want {
			t.Errorf("decode(%s) = %v, want %v", raw, got, want)
		}
	}
}

// TestALegacyCapabilitiesDocumentStillDecodes drives the whole struct the
// way the registry stores it, because that is the shape that broke.
func TestALegacyCapabilitiesDocumentStillDecodes(t *testing.T) {
	stored := `{"Search":1,"URLFetch":0,"Vision":2,"ToolUse":1,` +
		`"LongContext":0,"StructuredOutput":0,"CompliancePosture":{"AuthModes":["api-key"]}}`

	var got Capabilities
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatalf("decoding a row written by an earlier build: %v", err)
	}
	if got.Search != CapabilitySupported || got.Vision != CapabilityUnsupported {
		t.Errorf("a legacy row decoded to %+v", got)
	}
}

// TestAnUnknownEncodingIsAnErrorNotAnUnknownCapability: reading a value
// this build does not recognise as "not probed" turns a decoding failure
// into a plausible-looking capability, which for a tri-state whose point
// is that unknown is a distinct answer is the worse outcome.
func TestAnUnknownEncodingIsAnErrorNotAnUnknownCapability(t *testing.T) {
	for _, raw := range []string{`"probably"`, `7`, `-1`, `{}`, `true`} {
		var got CapabilityState
		err := json.Unmarshal([]byte(raw), &got)
		if err == nil {
			t.Errorf("decode(%s) succeeded as %v, want a refusal", raw, got)
			continue
		}
		if got != CapabilityUnknown {
			t.Errorf("decode(%s) failed but still wrote %v", raw, got)
		}
	}
}

// TestTheEncodingRoundTrips keeps the two halves in step.
func TestTheEncodingRoundTrips(t *testing.T) {
	want := Capabilities{
		Search: CapabilitySupported, Vision: CapabilityUnsupported,
		CompliancePosture: CompliancePosture{AuthModes: []string{"api-key"}},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"Search"`) {
		t.Errorf("the document carries Go's own field names: %s", raw)
	}
	var got Capabilities
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Search != want.Search || got.Vision != want.Vision {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
