package corpus

import "testing"

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

// TestTrustRank_OrdersLeastTrustedFirst pins the ordering resolveTrust
// depends on, including the below-everything rank an unrecognized value
// gets. If an unknown value ever ranked above untrusted-source, the
// less-trusted-wins comparison would start preferring it.
func TestTrustRank_OrdersLeastTrustedFirst(t *testing.T) {
	if trustRank("nonsense") >= trustRank(TrustUntrustedSource) {
		t.Error("an unrecognized trust value must rank below untrusted-source")
	}
	if trustRank(TrustUntrustedSource) >= trustRank(TrustTrusted) {
		t.Error("untrusted-source must rank below trusted")
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
