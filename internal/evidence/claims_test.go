package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClaimTypeValidAndDecode(t *testing.T) {
	for _, ct := range []ClaimType{ClaimObservedFact, ClaimInference, ClaimHypothesis} {
		if !ct.Valid() {
			t.Errorf("%q.Valid() = false, want true", ct)
		}
		if _, err := DecodeClaimType(string(ct)); err != nil {
			t.Errorf("DecodeClaimType(%q): %v", ct, err)
		}
	}
	for _, raw := range []string{"", "bogus", "OBSERVED_FACT"} {
		if _, err := DecodeClaimType(raw); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("DecodeClaimType(%q) = %v, want typed invalid-input", raw, err)
		}
	}
}

func TestVerificationResultValidAndDecode(t *testing.T) {
	for _, r := range []VerificationResult{VerificationCorroborated, VerificationContradicted, VerificationUnverified} {
		if !r.Valid() {
			t.Errorf("%q.Valid() = false, want true", r)
		}
	}
	if _, err := DecodeVerificationResult("unknown"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("DecodeVerificationResult(unknown) = %v, want typed invalid-input", err)
	}
}

func TestNewClaimIDPrefixAndLength(t *testing.T) {
	id, err := NewClaimID()
	if err != nil {
		t.Fatalf("NewClaimID: %v", err)
	}
	if !strings.HasPrefix(id, ClaimIDPrefix) {
		t.Errorf("NewClaimID() = %q, want prefix %q", id, ClaimIDPrefix)
	}
	if got, want := len(id), len(ClaimIDPrefix)+26; got != want {
		t.Errorf("len(NewClaimID()) = %d, want %d", got, want)
	}
	id2, err := NewClaimID()
	if err != nil {
		t.Fatalf("NewClaimID (2nd): %v", err)
	}
	if id == id2 {
		t.Error("two NewClaimID calls returned the same id")
	}
}

func validClaim() Claim {
	return Claim{
		ID:         "CLM-TEST",
		Statement:  "a statement",
		Type:       ClaimObservedFact,
		Confidence: 0.5,
		ProducedBy: ProducedBy{RunID: "run-1", LaneID: "lane-1"},
		DataClass:  DataClassInternal,
	}
}

func TestClaimValidate(t *testing.T) {
	if err := validClaim().Validate(); err != nil {
		t.Fatalf("valid claim: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(c Claim) Claim
	}{
		{"empty statement", func(c Claim) Claim { c.Statement = ""; return c }},
		{"unknown type", func(c Claim) Claim { c.Type = "bogus"; return c }},
		{"confidence too low", func(c Claim) Claim { c.Confidence = -0.1; return c }},
		{"confidence too high", func(c Claim) Claim { c.Confidence = 1.1; return c }},
		{"unknown data_class", func(c Claim) Claim { c.DataClass = "bogus"; return c }},
		{"empty run_id", func(c Claim) Claim { c.ProducedBy.RunID = ""; return c }},
		{"unknown verification result", func(c Claim) Claim {
			c.Verification = []Verification{{RunID: "r", Result: "bogus"}}
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mutate(validClaim()).Validate()
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("Validate() = %v, want typed invalid-input", err)
			}
		})
	}
}

// TestClaimSchemaRoundTripsAgainstGolden asserts Claim and Evidence carry
// exactly the R-21.41 field set as amended by R-21.94/R-21.86 -- no field
// added, none omitted -- by round-tripping the fixture through
// encoding/json and back.
func TestClaimSchemaRoundTripsAgainstGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "expand", "claim_v1_golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var fixture struct {
		Claim    Claim    `json:"claim"`
		Evidence Evidence `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if err := fixture.Claim.Validate(); err != nil {
		t.Fatalf("golden claim fails Validate: %v", err)
	}
	if err := fixture.Evidence.Validate(); err != nil {
		t.Fatalf("golden evidence fails Validate: %v", err)
	}

	reEncoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var roundTripped struct {
		Claim    Claim    `json:"claim"`
		Evidence Evidence `json:"evidence"`
	}
	if err := json.Unmarshal(reEncoded, &roundTripped); err != nil {
		t.Fatalf("unmarshal re-encoded: %v", err)
	}
	if roundTripped.Claim.ID != fixture.Claim.ID || roundTripped.Claim.DataClass != fixture.Claim.DataClass {
		t.Errorf("claim round trip mismatch: got %+v, want %+v", roundTripped.Claim, fixture.Claim)
	}
	if roundTripped.Evidence.CapturedRef != fixture.Evidence.CapturedRef ||
		roundTripped.Evidence.Source.Locator != fixture.Evidence.Source.Locator {
		t.Errorf("evidence round trip mismatch: got %+v, want %+v", roundTripped.Evidence, fixture.Evidence)
	}

	// Field-set fence: every top-level JSON key present in the fixture
	// must correspond to an actual struct field (an unknown key here
	// means either the fixture or the struct has drifted from R-21.41).
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal raw map: %v", err)
	}
	assertKeysExactly(t, rawMap["claim"], []string{
		"id", "statement", "type", "confidence", "produced_by", "verification",
		"contradictions", "data_class",
	})
	assertKeysExactly(t, rawMap["evidence"], []string{
		"id", "claim_id", "source", "content_hash", "observed_at", "data_class", "captured_ref",
	})
}

func assertKeysExactly(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	wantSet := make(map[string]bool, len(want))
	for _, k := range want {
		wantSet[k] = true
		if _, ok := m[k]; !ok {
			t.Errorf("golden fixture missing field %q", k)
		}
	}
	for k := range m {
		if !wantSet[k] {
			t.Errorf("golden fixture carries unexpected field %q", k)
		}
	}
}
