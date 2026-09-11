package registry

import (
	"testing"
	"time"
)

func TestDriverKindValid(t *testing.T) {
	valid := []DriverKind{DriverAnthropic, DriverOpenAICompat, DriverGemini, DriverOllama, DriverLocalLLM}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("DriverKind(%q).Valid() = false, want true", k)
		}
	}
	if DriverKind("bogus").Valid() {
		t.Error(`DriverKind("bogus").Valid() = true, want false`)
	}
	if DriverKind("").Valid() {
		t.Error(`DriverKind("").Valid() = true, want false`)
	}
}

func TestAuthTypeValid(t *testing.T) {
	if !AuthKey.Valid() || !AuthOAuth.Valid() {
		t.Error("AuthKey/AuthOAuth should be valid")
	}
	if AuthType("bogus").Valid() {
		t.Error(`AuthType("bogus").Valid() = true, want false`)
	}
}

func TestAccountKindValid(t *testing.T) {
	for _, k := range []AccountKind{AccountPersonal, AccountShared, AccountService} {
		if !k.Valid() {
			t.Errorf("AccountKind(%q).Valid() = false, want true", k)
		}
	}
	if AccountKind("bogus").Valid() {
		t.Error(`AccountKind("bogus").Valid() = true, want false`)
	}
}

func TestTierValid(t *testing.T) {
	for _, tr := range []Tier{TierCheapest, TierFree, TierCheap, TierMid, TierStrong, TierStrongest} {
		if !tr.Valid() {
			t.Errorf("Tier(%q).Valid() = false, want true", tr)
		}
	}
	if Tier("bogus").Valid() {
		t.Error(`Tier("bogus").Valid() = true, want false`)
	}
}

func TestHealthStatusValidZeroValueIsUnknown(t *testing.T) {
	if !HealthUnknown.Valid() {
		t.Error("HealthUnknown (zero value) should be valid")
	}
	if HealthUnknown != "" {
		t.Errorf("HealthUnknown = %q, want empty string (the honest default)", HealthUnknown)
	}
	for _, h := range []HealthStatus{HealthHealthy, HealthDegraded, HealthDead} {
		if !h.Valid() {
			t.Errorf("HealthStatus(%q).Valid() = false, want true", h)
		}
	}
	if HealthStatus("bogus").Valid() {
		t.Error(`HealthStatus("bogus").Valid() = true, want false`)
	}
}

func TestCapacityBucketValid(t *testing.T) {
	for _, c := range []CapacityBucket{CapacityInteractiveUsage, CapacityAgentSDKCredit, CapacityAPICredit} {
		if !c.Valid() {
			t.Errorf("CapacityBucket(%q).Valid() = false, want true", c)
		}
	}
	if CapacityBucket("bogus").Valid() {
		t.Error(`CapacityBucket("bogus").Valid() = true, want false`)
	}
}

// TestLaneStateNoPermissiveZeroValue asserts the contract's explicit
// requirement: the empty string is NOT a valid LaneState. A caller must
// write LaneStateUnknown explicitly for an unresolved state.
func TestLaneStateNoPermissiveZeroValue(t *testing.T) {
	if LaneState("").Valid() {
		t.Error(`LaneState("").Valid() = true, want false -- zero value must not be permissive`)
	}
	for _, s := range []LaneState{LaneStateAvailable, LaneStateConstrained, LaneStateExhausted, LaneStateAuthRequired, LaneStateUnknown} {
		if !s.Valid() {
			t.Errorf("LaneState(%q).Valid() = false, want true", s)
		}
	}
}

func TestVaultKeyRefStringIsSafeName(t *testing.T) {
	ref := VaultKeyRef("provider.anthropic.key")
	if ref.String() != "provider.anthropic.key" {
		t.Errorf("VaultKeyRef.String() = %q, want %q", ref.String(), "provider.anthropic.key")
	}
}

func TestCostRecordEncodeParseRoundtrip(t *testing.T) {
	rec := &CostRecord{
		Models: map[string]ModelCost{
			"claude-3-5-sonnet": {InputMicroUSDPerToken: 3000, OutputMicroUSDPerToken: 15000},
		},
		LastRefreshed: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	encoded, err := EncodeCostRecord(rec)
	if err != nil {
		t.Fatalf("EncodeCostRecord: %v", err)
	}
	got, err := ParseCostRecord(encoded)
	if err != nil {
		t.Fatalf("ParseCostRecord: %v", err)
	}
	if got == nil || got.Models["claude-3-5-sonnet"] != rec.Models["claude-3-5-sonnet"] {
		t.Errorf("ParseCostRecord roundtrip = %+v, want %+v", got, rec)
	}
	if !got.LastRefreshed.Equal(rec.LastRefreshed) {
		t.Errorf("LastRefreshed = %v, want %v", got.LastRefreshed, rec.LastRefreshed)
	}
}

func TestCostRecordNilMeansUnknown(t *testing.T) {
	encoded, err := EncodeCostRecord(nil)
	if err != nil || encoded != nil {
		t.Fatalf("EncodeCostRecord(nil) = (%v, %v), want (nil, nil)", encoded, err)
	}
	got, err := ParseCostRecord(nil)
	if err != nil || got != nil {
		t.Fatalf("ParseCostRecord(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestParseCostRecordMalformedNeverPanicsReturnsIntegrityError(t *testing.T) {
	_, err := ParseCostRecord([]byte(`{"models": "not-an-object"`))
	if err == nil {
		t.Fatal("ParseCostRecord on malformed JSON should return an error, got nil")
	}
}

func TestProviderRecordValidateRejectsMissingFields(t *testing.T) {
	valid := ProviderRecord{
		Name: "anthropic", Driver: DriverAnthropic, Auth: AuthKey, AuthRef: VaultKeyRef("provider.anthropic.key"),
		AccountKind: AccountPersonal, Tier: TierStrong, HealthStatus: HealthUnknown,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() on a well-formed record: %v", err)
	}

	cases := []struct {
		name string
		mut  func(ProviderRecord) ProviderRecord
	}{
		{"empty name", func(r ProviderRecord) ProviderRecord { r.Name = ""; return r }},
		{"bad driver", func(r ProviderRecord) ProviderRecord { r.Driver = "bogus"; return r }},
		{"bad auth", func(r ProviderRecord) ProviderRecord { r.Auth = "bogus"; return r }},
		{"empty auth_ref", func(r ProviderRecord) ProviderRecord { r.AuthRef = ""; return r }},
		{"bad account_kind", func(r ProviderRecord) ProviderRecord { r.AccountKind = "bogus"; return r }},
		{"bad tier", func(r ProviderRecord) ProviderRecord { r.Tier = "bogus"; return r }},
		{"bad health_status", func(r ProviderRecord) ProviderRecord { r.HealthStatus = "bogus"; return r }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.mut(valid).Validate(); err == nil {
				t.Errorf("Validate() with %s should have failed", c.name)
			}
		})
	}
}

func TestLaneRecordValidateRejectsMissingFields(t *testing.T) {
	valid := LaneRecord{
		LaneName: "anthropic", ProviderName: "anthropic",
		Capacity: CapacityInteractiveUsage, State: LaneStateAvailable,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() on a well-formed lane: %v", err)
	}

	cases := []struct {
		name string
		mut  func(LaneRecord) LaneRecord
	}{
		{"empty lane_name", func(r LaneRecord) LaneRecord { r.LaneName = ""; return r }},
		{"empty provider_name", func(r LaneRecord) LaneRecord { r.ProviderName = ""; return r }},
		{"bad capacity", func(r LaneRecord) LaneRecord { r.Capacity = "bogus"; return r }},
		{"empty state (permissive zero value)", func(r LaneRecord) LaneRecord { r.State = ""; return r }},
		{"bad state", func(r LaneRecord) LaneRecord { r.State = "bogus"; return r }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.mut(valid).Validate(); err == nil {
				t.Errorf("Validate() with %s should have failed", c.name)
			}
		})
	}
}

// FuzzCostRecord asserts ParseCostRecord never panics on arbitrary bytes
// (Art.1: "malformed cost JSON is handled without panic").
func FuzzCostRecord(f *testing.F) {
	f.Add([]byte(`{"models":{"m":{"input_micro_usd_per_token":1,"output_micro_usd_per_token":2}},"last_refreshed":"2026-09-01T00:00:00Z"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := ParseCostRecord(data)
		if err != nil && rec != nil {
			t.Fatalf("ParseCostRecord returned a non-nil record alongside an error: %v, %+v", err, rec)
		}
	})
}
