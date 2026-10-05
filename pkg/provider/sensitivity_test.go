package provider_test

// Purpose (this file): the closed SensitivityTier parse, its text and
//   legacy-number JSON forms, the fail-closed joins, the ModelRequest
//   persisted form, and the run.go flag row moved here from cmd/cascade
//   (P1-BF-R121: the row lives with the parser the flag calls).
// Inputs: literal tier names and JSON fixtures; the ModelRequest fixture is
//   json.Marshal output at f688c0b (evidence run4/fixtures-provenance.txt).
// Outputs: assertions only.
// Constraints: refusals are checked by Kind AND message, never by
//   errors.Is alone (cascade sentinels compare Kind only).
// SPORT: pkg.provider.sensitivity-tier/CHANGE (P1-SEC-19).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// requireInvalidInput fails t unless err is KindInvalidInput and its
// message contains every fragment.
func requireInvalidInput(t *testing.T, err error, fragments ...string) {
	t.Helper()
	requireKind(t, err, cascade.KindInvalidInput, fragments...)
}

// requireKind fails t unless err carries kind and its message contains
// every fragment.
func requireKind(t *testing.T, err error, kind cascade.Kind, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a %v error, got nil", kind)
	}
	if !cascade.HasKind(err, kind) {
		t.Fatalf("error %v: want %v", err, kind)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("error %q must contain %q", err.Error(), f)
		}
	}
}

// TestParseSensitivityTierClosed pins the closed parse: the four exact
// names, "" as the restricted zero value, and every other spelling as
// local-only WITH an error naming the value.
func TestParseSensitivityTierClosed(t *testing.T) {
	for _, bad := range []string{"RESTRICTED ", "Restricted", "secret", "normal", "local_only", " public", "LOCAL-ONLY"} {
		got, err := provider.ParseSensitivityTier(bad)
		if got != provider.SensitivityLocalOnly {
			t.Errorf("ParseSensitivityTier(%q) = %v, want local-only", bad, got)
		}
		requireInvalidInput(t, err, `"`+bad+`"`, "is not a tier")
	}
	got, err := provider.ParseSensitivityTier("")
	if got != provider.SensitivityRestricted || err != nil {
		t.Fatalf(`ParseSensitivityTier("") = %v, %v; want restricted, nil`, got, err)
	}
	for _, name := range []string{"restricted", "local-only", "internal", "public"} {
		got, err := provider.ParseSensitivityTier(name)
		if err != nil || got.String() != name {
			t.Fatalf("ParseSensitivityTier(%q) = %v, %v; want the named tier", name, got, err)
		}
	}
}

// TestSensitivityTierTextRoundTrip pins MarshalText/UnmarshalText: every
// member round-trips by name, an out-of-range value refuses to marshal, and
// a failed unmarshal leaves local-only behind, not the zero value.
func TestSensitivityTierTextRoundTrip(t *testing.T) {
	for t0 := provider.SensitivityRestricted; t0.Valid(); t0++ {
		b, err := t0.MarshalText()
		if err != nil || string(b) != t0.String() {
			t.Fatalf("MarshalText(%d) = %q, %v; want %q", uint8(t0), b, err, t0.String())
		}
		var back provider.SensitivityTier
		if err := back.UnmarshalText(b); err != nil || back != t0 {
			t.Fatalf("UnmarshalText(%q) = %v, %v; want %v", b, back, err, t0)
		}
	}
	for _, bad := range []provider.SensitivityTier{4, 9, 255} {
		if _, err := bad.MarshalText(); err == nil {
			t.Fatalf("MarshalText(%d) succeeded; want a refusal", uint8(bad))
		} else {
			requireInvalidInput(t, err, "is not a tier")
		}
	}
	back := provider.SensitivityPublic
	requireInvalidInput(t, back.UnmarshalText([]byte("Public")), `"Public"`)
	if back != provider.SensitivityLocalOnly {
		t.Fatalf("failed UnmarshalText left %v, want local-only", back)
	}
}

// TestSensitivityTierLegacyJSONNumber pins UnmarshalJSON: a JSON string by
// name, the legacy numbers 0..3 written before the text form existed, and
// a KindIntegrity refusal (with local-only left behind) for every other
// name, number or type: a JSON tier comes from a stored record.
func TestSensitivityTierLegacyJSONNumber(t *testing.T) {
	for raw, want := range map[string]provider.SensitivityTier{
		`0`: provider.SensitivityRestricted, `1`: provider.SensitivityLocalOnly,
		`2`: provider.SensitivityInternal, `3`: provider.SensitivityPublic,
		`"internal"`: provider.SensitivityInternal, `""`: provider.SensitivityRestricted,
	} {
		var got provider.SensitivityTier
		if err := json.Unmarshal([]byte(raw), &got); err != nil || got != want {
			t.Fatalf("Unmarshal(%s) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{`4`, `255`, `256`, `-1`, `1.0`, `1e0`, `true`, `"secret"`, `"RESTRICTED "`, `[]`} {
		got := provider.SensitivityPublic
		err := json.Unmarshal([]byte(raw), &got)
		requireKind(t, err, cascade.KindIntegrity)
		if got != provider.SensitivityLocalOnly {
			t.Fatalf("Unmarshal(%s) left %v, want local-only", raw, got)
		}
	}
}

// TestJoinFailClosed pins both joins' vacuity and contamination cases: the
// empty join is the narrowest answer, one empty or untrusted provenance
// makes the whole join untrusted, and an invalid tier joins as local-only.
func TestJoinFailClosed(t *testing.T) {
	if got := provider.JoinProvenance(provider.ProvenanceTrusted, ""); got != provider.ProvenanceUntrustedSource {
		t.Fatalf(`JoinProvenance(trusted, "") = %q, want untrusted-source`, got)
	}
	if got := provider.JoinProvenance(); got != provider.ProvenanceUntrustedSource {
		t.Fatalf("JoinProvenance() = %q, want untrusted-source", got)
	}
	if got := provider.JoinProvenance(provider.ProvenanceTrusted, "Trusted"); got != provider.ProvenanceUntrustedSource {
		t.Fatalf("JoinProvenance(trusted, Trusted) = %q, want untrusted-source", got)
	}
	if got := provider.JoinProvenance(provider.ProvenanceTrusted, provider.ProvenanceTrusted); got != provider.ProvenanceTrusted {
		t.Fatalf("JoinProvenance(trusted, trusted) = %q, want trusted", got)
	}
	if got := provider.JoinSensitivity(); got != provider.SensitivityLocalOnly {
		t.Fatalf("JoinSensitivity() = %v, want local-only", got)
	}
	for _, tc := range []struct {
		in   []provider.SensitivityTier
		want provider.SensitivityTier
	}{
		{[]provider.SensitivityTier{provider.SensitivityPublic, provider.SensitivityInternal}, provider.SensitivityInternal},
		{[]provider.SensitivityTier{provider.SensitivityInternal, provider.SensitivityRestricted}, provider.SensitivityRestricted},
		{[]provider.SensitivityTier{provider.SensitivityRestricted, provider.SensitivityLocalOnly}, provider.SensitivityLocalOnly},
		{[]provider.SensitivityTier{provider.SensitivityPublic}, provider.SensitivityPublic},
		{[]provider.SensitivityTier{provider.SensitivityPublic, 9}, provider.SensitivityLocalOnly},
	} {
		if got := provider.JoinSensitivity(tc.in...); got != tc.want {
			t.Fatalf("JoinSensitivity(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// legacyModelRequest is json.Marshal(ModelRequest{TaskID: "t1", TaskClass:
// "chat", Sensitivity: SensitivityLocalOnly}) at f688c0b, when the tier
// had no text form and encoded as its number.
const legacyModelRequest = `{"task_id":"t1","task_class":"chat","inputs":null,"requirements":{"reasoning":"","context":0,"structured":false},"sensitivity":1,"policy":{"external_allowed":false},"required_capabilities":{"Search":false,"URLFetch":false,"Vision":false,"ToolUse":false,"LongContext":false,"StructuredOutput":false}}`

// TestPersistedSensitivityFormsUnchanged is this package's persisted-form
// row: a legacy numeric ModelRequest decodes to local-only and re-encodes
// with the name in place of the number (and nothing else changed); a
// numeric 4 or 255, or an unknown name, decodes to local-only with a
// KindIntegrity refusal naming the value.
func TestPersistedSensitivityFormsUnchanged(t *testing.T) {
	var req provider.ModelRequest
	if err := json.Unmarshal([]byte(legacyModelRequest), &req); err != nil {
		t.Fatalf("decode legacy ModelRequest: %v", err)
	}
	if req.Sensitivity != provider.SensitivityLocalOnly {
		t.Fatalf("legacy sensitivity 1 decoded to %v, want local-only", req.Sensitivity)
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	want := strings.Replace(legacyModelRequest, `"sensitivity":1`, `"sensitivity":"local-only"`, 1)
	if string(out) != want {
		t.Fatalf("re-encoded ModelRequest:\n got %s\nwant %s", out, want)
	}
	for _, n := range []string{"4", "255", `"secret"`, `"RESTRICTED "`} {
		var bad provider.ModelRequest
		raw := strings.Replace(legacyModelRequest, `"sensitivity":1`, `"sensitivity":`+n, 1)
		requireKind(t, json.Unmarshal([]byte(raw), &bad), cascade.KindIntegrity, strings.Trim(n, `"`))
		if bad.Sensitivity != provider.SensitivityLocalOnly {
			t.Fatalf("stored sensitivity %s decoded to %v, want local-only", n, bad.Sensitivity)
		}
	}
}

// TestFormerParseSitesFailClosed holds the run.go flag row (moved from
// cmd/cascade, P1-BF-R121): `cascade run --sensitivity` validates through
// ParseSensitivityTier with no local default (the source shape is pinned
// by internal/build's TestArchOneSensitivityType_RealTreeGreen), so the
// row is the parser's answer to the flag's inputs: an unknown flag value
// refuses and an empty one is restricted.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("run_go_flag", func(t *testing.T) {
		for _, flag := range []string{"RESTRICTED ", "Restricted", "secret", "normal"} {
			got, err := provider.ParseSensitivityTier(flag)
			requireInvalidInput(t, err, `"`+flag+`"`)
			if got != provider.SensitivityLocalOnly {
				t.Fatalf("--sensitivity %q parsed to %v, want local-only", flag, got)
			}
		}
		if got, err := provider.ParseSensitivityTier(""); err != nil || got != provider.SensitivityRestricted {
			t.Fatalf("--sensitivity unset = %v, %v; want restricted, nil", got, err)
		}
	})
}
