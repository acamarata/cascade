package corpus

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestTrustLevel_SpelledExactlyAsThePlanNamesThem pins the two values
// against the spelling the phase plan uses ("trusted | untrusted-source"),
// written out as literals here rather than derived from the constants.
// A table that asserts a constant against itself passes while the constant
// is wrong, and the tag these strings name is what a downstream consumer
// matches on when it decides whether text may be obeyed.
func TestTrustLevel_SpelledExactlyAsThePlanNamesThem(t *testing.T) {
	if string(TrustTrusted) != "trusted" {
		t.Errorf("TrustTrusted = %q, want %q", string(TrustTrusted), "trusted")
	}
	if string(TrustUntrustedSource) != "untrusted-source" {
		t.Errorf("TrustUntrustedSource = %q, want %q", string(TrustUntrustedSource), "untrusted-source")
	}
}

// TestTrustLevel_Valid covers the closed set: exactly two members, and
// every other input including the zero value is not a level.
func TestTrustLevel_Valid(t *testing.T) {
	valid := []TrustLevel{"trusted", "untrusted-source"}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("%q should be a valid trust level", string(v))
		}
	}
	invalid := []TrustLevel{"", "unknown", "Trusted", "TRUSTED", "untrusted", "untrusted_source", "trusted "}
	for _, v := range invalid {
		if v.Valid() {
			t.Errorf("%q should not be a valid trust level", string(v))
		}
	}
}

// TestTrustLevel_String_NeverInventsASpelling proves an unrecognized value
// does not round-trip as though it were real. A String that echoed its
// receiver would let an unknown value be written back to storage and read
// again later as a value someone assumes was validated once.
func TestTrustLevel_String_NeverInventsASpelling(t *testing.T) {
	if got := TrustTrusted.String(); got != "trusted" {
		t.Errorf("TrustTrusted.String() = %q, want trusted", got)
	}
	if got := TrustUntrustedSource.String(); got != "untrusted-source" {
		t.Errorf("TrustUntrustedSource.String() = %q, want untrusted-source", got)
	}
	for _, bad := range []TrustLevel{"", "unknown", "trusted-ish"} {
		if got := bad.String(); got != "invalid" {
			t.Errorf("TrustLevel(%q).String() = %q, want invalid", string(bad), got)
		}
	}
}

// TestResolveTrust_FailsClosed is the core authorization property of this
// dimension: every combination that is not "both sides say trusted"
// resolves to untrusted-source. Unknown, empty and malformed values are
// all covered explicitly, because the failure this guards against is a
// helper answering "allowed" for input it could not decode.
func TestResolveTrust_FailsClosed(t *testing.T) {
	cases := []struct {
		name         string
		record       TrustLevel
		corpusSource TrustLevel
		want         TrustLevel
	}{
		{"both trusted", TrustTrusted, TrustTrusted, TrustTrusted},
		{"record untrusted in trusted corpus", TrustUntrustedSource, TrustTrusted, TrustUntrustedSource},
		{"trusted record in untrusted corpus", TrustTrusted, TrustUntrustedSource, TrustUntrustedSource},
		{"both untrusted", TrustUntrustedSource, TrustUntrustedSource, TrustUntrustedSource},
		{"record unset", "", TrustTrusted, TrustUntrustedSource},
		{"corpus unset", TrustTrusted, "", TrustUntrustedSource},
		{"both unset", "", "", TrustUntrustedSource},
		{"record unknown value", "definitely-fine", TrustTrusted, TrustUntrustedSource},
		{"corpus unknown value", TrustTrusted, "definitely-fine", TrustUntrustedSource},
		{"wrong case", "Trusted", "Trusted", TrustUntrustedSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTrust(tc.record, tc.corpusSource); got != tc.want {
				t.Errorf("resolveTrust(%q, %q) = %q, want %q",
					string(tc.record), string(tc.corpusSource), string(got), string(tc.want))
			}
		})
	}
}

// TestFormerParseSitesFailClosed is this package's row of the former-
// parse-site table: resolveTrust. An unknown and an empty tag on either
// side yield untrusted-source, the most restrictive outcome; restoring a
// local ranking that let an unknown value outrank untrusted-source turns
// it red.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("corpus_resolveTrust", func(t *testing.T) {
		for _, odd := range []TrustLevel{"", "Trusted", "trusted ", "verified"} {
			if got := resolveTrust(odd, TrustTrusted); got != TrustUntrustedSource {
				t.Errorf("resolveTrust(%q, trusted) = %q, want untrusted-source", string(odd), string(got))
			}
			if got := resolveTrust(TrustTrusted, odd); got != TrustUntrustedSource {
				t.Errorf("resolveTrust(trusted, %q) = %q, want untrusted-source", string(odd), string(got))
			}
		}
	})
}

// TestTrustLevelIsTheProviderType pins the alias as type identity: a
// provider.Provenance IS a TrustLevel, and the constants are the same
// values, so there is no conversion anywhere between the two.
func TestTrustLevelIsTheProviderType(t *testing.T) {
	passThrough := func(p provider.Provenance) TrustLevel { return p } // compiles only for an alias
	if passThrough(TrustTrusted) != provider.ProvenanceTrusted || TrustUntrustedSource != provider.ProvenanceUntrustedSource {
		t.Fatal("corpus trust constants are not the provider provenance members")
	}
}

// TestValidateCorpusKind_FailsClosed keeps the corpus-kind check closed the
// same way trust is: the registered kinds pass, and an empty, near or
// unknown kind is refused rather than read as an empty corpus.
func TestValidateCorpusKind_FailsClosed(t *testing.T) {
	for _, kind := range []string{CorpusIDCode, CorpusIDGraph} {
		if err := ValidateCorpusKind(kind); err != nil {
			t.Fatalf("ValidateCorpusKind(%q) = %v, want nil", kind, err)
		}
	}
	for _, kind := range []string{"", "Code", "docs"} {
		if err := ValidateCorpusKind(kind); err == nil {
			t.Fatalf("ValidateCorpusKind(%q) accepted an unregistered kind", kind)
		}
	}
}

// Persisted corpus encodings: json.Marshal output at f688c0b, when
// TrustLevel was its own string type (evidence run4/fixtures-provenance.txt).
const (
	persistedCorpus = `{"id":"docs","scope_ref":"project:one","privacy":"project","visibility":"scope-local","trust":"trusted"}`
	persistedRecord = `{"id":"docs/readme.md#1","corpus_id":"docs","scope_ref":"project:one","privacy":"project","visibility":"scope-local","trust":"untrusted-source"}`
)

// TestPersistedSensitivityFormsUnchanged is this package's persisted-form
// row: the json:"trust" field of a stored Corpus and Record decodes to the
// same level and re-encodes byte-identical under the alias.
func TestPersistedSensitivityFormsUnchanged(t *testing.T) {
	var c Corpus
	if err := json.Unmarshal([]byte(persistedCorpus), &c); err != nil || c.Trust != TrustTrusted {
		t.Fatalf("decode corpus = %+v, %v; want trust trusted", c, err)
	}
	var r Record
	if err := json.Unmarshal([]byte(persistedRecord), &r); err != nil || r.Trust != TrustUntrustedSource {
		t.Fatalf("decode record = %+v, %v; want trust untrusted-source", r, err)
	}
	for raw, v := range map[string]any{persistedCorpus: c, persistedRecord: r} {
		out, err := json.Marshal(v)
		if err != nil || string(out) != raw {
			t.Fatalf("re-encode:\n got %s (%v)\nwant %s", out, err, raw)
		}
	}
}

// seededStore holds one trusted record and one untrusted-source record in
// the same trusted corpus, plus a personal-tier record, so the propagation
// and entitlement assertions run against a mixed corpus.
func seededStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	if err := s.AddCorpus(validCorpus()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRecord(validRecord()); err != nil {
		t.Fatal(err)
	}
	untrusted := validRecord()
	untrusted.ID = "docs/vendor.md#1"
	untrusted.Trust = TrustUntrustedSource
	if err := s.AddRecord(untrusted); err != nil {
		t.Fatal(err)
	}
	personal := validRecord()
	personal.ID = "docs/notes.md#1"
	personal.Privacy = PrivacyPersonal
	if err := s.AddRecord(personal); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestStore_Query_UntrustedTagSurvivesTheQueryAPI is the plan's acceptance
// criterion. A record tagged untrusted-source must arrive at the consumer
// still tagged: context assembly and the auto-advance ceiling can only
// refuse to obey untrusted instructions if the tag is still attached when
// they see the record.
func TestStore_Query_UntrustedTagSurvivesTheQueryAPI(t *testing.T) {
	got, err := seededStore(t).Query(Query{Membership: ownMembership(), Entitlement: PrivacyPersonal})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	tags := map[string]TrustLevel{}
	for _, r := range got {
		tags[r.ID] = r.Trust
	}
	if len(tags) != 3 {
		t.Fatalf("query returned %d records, want 3", len(tags))
	}
	if tags["docs/vendor.md#1"] != TrustUntrustedSource {
		t.Errorf("the untrusted-source record surfaced as %q, want untrusted-source",
			tags["docs/vendor.md#1"].String())
	}
	if tags["docs/readme.md#1"] != TrustTrusted {
		t.Errorf("the trusted record surfaced as %q, want trusted", tags["docs/readme.md#1"].String())
	}
}

// TestStore_Query_UntrustedCorpusTaintsEveryRecord is the other half of
// propagation: a record cannot claim to be trusted when the source it came
// from is not.
func TestStore_Query_UntrustedCorpusTaintsEveryRecord(t *testing.T) {
	s := NewStore()
	c := validCorpus()
	c.Trust = TrustUntrustedSource
	if err := s.AddCorpus(c); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRecord(validRecord()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(Query{Membership: ownMembership(), Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("query returned %d records, want 1", len(got))
	}
	if got[0].Trust != TrustUntrustedSource {
		t.Errorf("a record from an untrusted corpus surfaced as %q", got[0].Trust.String())
	}
}
