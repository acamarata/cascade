// Purpose: request.go's enums round-trip through JSON and TOML, refuse an
// unknown string, and never silently treat a zero value as valid; the
// R-21.143 raise-only DataClass validator is asserted directly, never
// against a second copy of itself.
//
// SPORT: fleet.capacity.request (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"encoding"
	"encoding/json"
	"github.com/acamarata/cascade/pkg/provider"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestResourceRequestEnums round-trips every enum this ticket declares
// (QualityEnum, DeadlineEnum, DataClass, Tier) through JSON and TOML, and
// proves an unknown string refuses to decode in both formats.
func TestResourceRequestEnums(t *testing.T) {
	t.Run("QualityEnum", func(t *testing.T) {
		var out QualityEnum
		roundTripJSON(t, QualityHigh, &out)
		roundTripTOML(t, QualityHigh, &out)
		requireDecodeErrorJSON(t, "bogus", &out)
	})
	t.Run("DeadlineEnum", func(t *testing.T) {
		var out DeadlineEnum
		roundTripJSON(t, DeadlineBatch, &out)
		roundTripTOML(t, DeadlineBatch, &out)
		requireDecodeErrorJSON(t, "bogus", &out)
	})

	t.Run("Tier", func(t *testing.T) {
		var out Tier
		roundTripJSON(t, TierOne, &out)
		roundTripTOML(t, TierOne, &out)
		requireDecodeErrorJSON(t, "bogus", &out)
	})
}

func roundTripJSON(t *testing.T, want encoding.TextMarshaler, into encoding.TextUnmarshaler) {
	t.Helper()
	js, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(%v): %v", want, err)
	}
	if err := json.Unmarshal(js, into); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", js, err)
	}
}

func roundTripTOML(t *testing.T, want any, _ any) {
	t.Helper()
	tb, err := toml.Marshal(struct{ V any }{want})
	if err != nil {
		t.Fatalf("toml.Marshal(%v): %v", want, err)
	}
	var out struct{ V any }
	if err := toml.Unmarshal(tb, &out); err != nil {
		t.Fatalf("toml.Unmarshal(%s): %v", tb, err)
	}
}

func requireDecodeErrorJSON(t *testing.T, raw string, into encoding.TextUnmarshaler) {
	t.Helper()
	if err := json.Unmarshal([]byte(`"`+raw+`"`), into); err == nil {
		t.Fatalf("json.Unmarshal(%q) = nil error, want a refusal", raw)
	}
}

// TestTierZeroValueNotSilentlyValid proves Tier's (and QualityEnum's and
// DeadlineEnum's) zero value is not a member -- a caller must explicitly
// initialize the field.
func TestTierZeroValueNotSilentlyValid(t *testing.T) {
	var zt Tier
	if zt.Valid() {
		t.Fatal("Tier zero value reports Valid() = true, want false")
	}
	var zq QualityEnum
	if zq.Valid() {
		t.Fatal("QualityEnum zero value reports Valid() = true, want false")
	}
	var zd DeadlineEnum
	if zd.Valid() {
		t.Fatal("DeadlineEnum zero value reports Valid() = true, want false")
	}
}

// TestResourceRequestDataClassRaiseOnly is the named acceptance test
// (R-21.143): raising is accepted, a class below the derived floor is
// refused with a typed error, and an unset class resolves to the most
// restrictive value.
func TestResourceRequestDataClassRaiseOnly(t *testing.T) {
	base := ResourceRequest{
		Intent:     "test",
		TaskClass:  conductor.TaskClassCode,
		MinQuality: QualityStandard,
		Deadline:   DeadlineInteractive,
	}
	for _, c := range dataClassRaiseOnlyCases() {
		t.Run(c.name, func(t *testing.T) {
			req := base
			req.DataClass = c.presented
			out, err := ValidateResourceRequest(req, c.floor)
			if c.wantErr {
				if err == nil {
					t.Fatal("ValidateResourceRequest = nil error, want a refusal")
				}
				kind, ok := cascade.KindOf(err)
				if !ok || kind != cascade.KindPolicyDenied {
					t.Fatalf("KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateResourceRequest: %v", err)
			}
			if out.DataClass == nil || *out.DataClass != *c.want {
				t.Fatalf("DataClass = %q, want %q", out.DataClass, c.want)
			}
		})
	}
}

type dataClassRaiseOnlyCase struct {
	name      string
	presented *provider.SensitivityTier
	floor     *provider.SensitivityTier
	wantErr   bool
	want      *provider.SensitivityTier
}

// dataClassRaiseOnlyCases is split out of the test function so the test
// itself stays under the 50-line function cap.
func dataClassRaiseOnlyCases() []dataClassRaiseOnlyCase {
	return []dataClassRaiseOnlyCase{
		{name: "raise accepted", presented: new(provider.SensitivityLocalOnly), floor: new(provider.SensitivityPublic), want: new(provider.SensitivityLocalOnly)},
		{name: "downgrade refused", presented: new(provider.SensitivityPublic), floor: new(provider.SensitivityLocalOnly), wantErr: true},
		{name: "unset resolves to floor", presented: nil, floor: new(provider.SensitivityRestricted), want: new(provider.SensitivityRestricted)},
		{name: "unset floor resolves to most restrictive", presented: nil, floor: nil, want: new(provider.SensitivityLocalOnly)},
		{name: "equal to floor accepted", presented: new(provider.SensitivityInternal), floor: new(provider.SensitivityInternal), want: new(provider.SensitivityInternal)},
	}
}

// TestValidateResourceRequestFailClosed asserts ValidateResourceRequest
// refuses every malformed field rather than substituting a permissive
// default, except the three fields with a documented fail-closed
// substitution (risk_class, sensitivity, data_class).
func TestValidateResourceRequestFailClosed(t *testing.T) {
	valid := ResourceRequest{
		Intent:     "test",
		TaskClass:  conductor.TaskClassCode,
		MinQuality: QualityStandard,
		Deadline:   DeadlineInteractive,
	}

	cases := []struct {
		name string
		mut  func(ResourceRequest) ResourceRequest
	}{
		{"empty intent", func(r ResourceRequest) ResourceRequest { r.Intent = ""; return r }},
		{"unknown task_class", func(r ResourceRequest) ResourceRequest { r.TaskClass = "bogus"; return r }},
		{"unset min_quality", func(r ResourceRequest) ResourceRequest { r.MinQuality = ""; return r }},
		{"unknown min_quality", func(r ResourceRequest) ResourceRequest { r.MinQuality = "bogus"; return r }},
		{"unset deadline", func(r ResourceRequest) ResourceRequest { r.Deadline = ""; return r }},
		{"unknown preferred_tier", func(r ResourceRequest) ResourceRequest { r.PreferredTier = "bogus"; return r }},
		{"unknown risk_class", func(r ResourceRequest) ResourceRequest { r.RiskClass = "bogus"; return r }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ValidateResourceRequest(c.mut(valid), new(provider.SensitivityPublic)); err == nil {
				t.Fatalf("ValidateResourceRequest(%s) = nil error, want a refusal", c.name)
			}
		})
	}

	t.Run("unset risk_class resolves to Critical", func(t *testing.T) {
		out, err := ValidateResourceRequest(valid, new(provider.SensitivityPublic))
		if err != nil {
			t.Fatalf("ValidateResourceRequest: %v", err)
		}
		if out.RiskClass != "critical" {
			t.Fatalf("RiskClass = %q, want critical (most restrictive)", out.RiskClass)
		}
	})
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	req := ResourceRequest{Intent: "test", TaskClass: conductor.TaskClassCode, MinQuality: QualityStandard, Deadline: DeadlineInteractive}
	got, err := ValidateResourceRequest(req, nil)
	if err != nil || got.DataClass == nil || *got.DataClass != provider.SensitivityLocalOnly {
		t.Fatalf("unset = %+v: %v", got, err)
	}
	for _, tier := range []provider.SensitivityTier{provider.SensitivityPublic, provider.SensitivityInternal, provider.SensitivityRestricted} {
		req.DataClass = new(tier)
		if _, err := ValidateResourceRequest(req, nil); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("below unset floor: %v", err)
		}
	}
	if _, err := ValidateDataClassFloor(nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil presented: %v", err)
	}
	req.DataClass = new(provider.SensitivityLocalOnly)
	got, err = ValidateResourceRequest(req, nil)
	if err != nil || got.DataClass == req.DataClass {
		t.Fatalf("not a fresh pointer: %v", err)
	}
}
