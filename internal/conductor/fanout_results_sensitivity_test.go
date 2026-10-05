package conductor

// Purpose (this file): row [5]: contract:sensitivity-tier covers the
//   persisted fan-out leg record (R8 routing). A LegResult at each tier
//   round-trips through JSON, and a record written before the tier had a
//   text form (numeric "Sensitivity") still decodes.
// Inputs: LegResult values; the legacy record is json.Marshal output at
//   9270595, the last P1-CORE-18 encoding (evidence
//   run4/fixtures-provenance.txt).
// Outputs: assertions only.
// Constraints: contract:fanout-leg-results is unchanged; only the tier's
//   wire spelling moves from number to name.
// SPORT: conductor.fanout leg-result sensitivity (CHANGE, P1-SEC-19).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// legacyLegResult is a LegResult at SensitivityLocalOnly as P1-CORE-18
// wrote it, with the tier encoded as its number.
const legacyLegResult = `{"FanOutID":"f1","TaskID":"t1","LegIndex":2,"Attempt":1,"RequestDigest":"sha256:ab","Sensitivity":1,"Response":{"job_id":"","selection":{"lane_id":"","provider":"","model":""},"output":"","usage":{"input_tokens":0,"output_tokens":0}}}`

// TestSensitivityCoversFanoutLegRecords pins the leg record's tier through
// persistence: every member round-trips by name, the legacy numeric record
// decodes to local-only and re-encodes by name with nothing else changed,
// an out-of-range tier does not marshal, and a stored record with an
// unknown name or number is refused as KindIntegrity.
func TestSensitivityCoversFanoutLegRecords(t *testing.T) {
	for tier := provider.SensitivityRestricted; tier.Valid(); tier++ {
		in := LegResult{FanOutID: "f1", TaskID: "t1", LegIndex: 2, Attempt: 1, RequestDigest: "sha256:ab", Sensitivity: tier}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal %v: %v", tier, err)
		}
		if !strings.Contains(string(raw), `"Sensitivity":"`+tier.String()+`"`) {
			t.Fatalf("leg record %s does not carry the tier by name", raw)
		}
		var out LegResult
		if err := json.Unmarshal(raw, &out); err != nil || !reflect.DeepEqual(out, in) {
			t.Fatalf("round trip %v = %+v, %v; want %+v", tier, out, err, in)
		}
	}
	var legacy LegResult
	if err := json.Unmarshal([]byte(legacyLegResult), &legacy); err != nil {
		t.Fatalf("decode legacy leg record: %v", err)
	}
	if legacy.Sensitivity != provider.SensitivityLocalOnly || legacy.LegIndex != 2 {
		t.Fatalf("legacy leg record decoded to %+v, want leg 2 at local-only", legacy)
	}
	again, err := json.Marshal(legacy)
	want := strings.Replace(legacyLegResult, `"Sensitivity":1`, `"Sensitivity":"local-only"`, 1)
	if err != nil || string(again) != want {
		t.Fatalf("re-encoded legacy record:\n got %s (%v)\nwant %s", again, err, want)
	}
	if _, err := json.Marshal(LegResult{Sensitivity: 9}); err == nil {
		t.Fatal("a leg record with tier 9 marshaled")
	}
	for _, stored := range []string{`255`, `"RESTRICTED "`, `"secret"`} {
		var bad LegResult
		err = json.Unmarshal([]byte(strings.Replace(legacyLegResult, `"Sensitivity":1`, `"Sensitivity":`+stored, 1)), &bad)
		if err == nil || !cascade.HasKind(err, cascade.KindIntegrity) || bad.Sensitivity != provider.SensitivityLocalOnly {
			t.Fatalf("stored tier %s decoded to %v, %v; want local-only and KindIntegrity", stored, bad.Sensitivity, err)
		}
	}
}
