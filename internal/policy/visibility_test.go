// Purpose: the TEAM carrier's contract. The expectations below are taken
// from the SPEC text — 04 §Epic F S-10.T4 ("corpus/scope model + privacy
// flags + TRUST dimension ... visibility classes (TEAM carrier w/
// I-S17.T1). AC incl. untrusted-tag propagation test") and the corpus
// package's own stated rules (classes ordered by reach; an unreadable
// value resolves to the narrowest; a record cannot out-rank its corpus) —
// and are written out literally rather than derived from this package's
// tables, so the test cannot agree with an implementation that is wrong.
//
// Per Art.2 the cross-epic seam is exercised with a REAL F/S-10.T4 scope
// record: the records here come back from corpus.Store.Query, already
// resolved against their corpus, which is exactly what production hands
// the carrier.
//
// SPORT: internal/policy TeamCarrier/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// teamScope is the scope every record and corpus in this file lives in.
const teamScope = corpus.ScopeRef("/repo/cascade")

// queryOne builds a real corpus.Store holding one corpus and one record,
// runs a real scope-filtered query against it, and returns the single
// RESOLVED record. Anything the store withholds fails the test loudly,
// because a carrier test that silently ran on nothing proves nothing.
func queryOne(t *testing.T, c corpus.Corpus, r corpus.Record) corpus.Record {
	t.Helper()
	s := corpus.NewStore()
	if err := s.AddCorpus(c); err != nil {
		t.Fatalf("AddCorpus: %v", err)
	}
	if err := s.AddRecord(r); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	got, err := s.Query(corpus.Query{
		Membership:  corpus.Membership{Scope: teamScope},
		Entitlement: corpus.PrivacyPersonal,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d records, want 1", len(got))
	}
	return got[0]
}

// teamCorpus is a trusted, team-visible, project-tier corpus.
func teamCorpus() corpus.Corpus {
	return corpus.Corpus{
		ID: "c1", ScopeRef: teamScope,
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityTeam, Trust: corpus.TrustTrusted,
	}
}

// recordIn builds a record in c1 with the given classification.
func recordIn(v corpus.VisibilityClass, p corpus.PrivacyClass, tr corpus.TrustLevel) corpus.Record {
	return corpus.Record{
		ID: "r1", CorpusID: "c1", ScopeRef: teamScope,
		Privacy: p, Visibility: v, Trust: tr,
	}
}

// carrierFor wraps the record a real query resolved.
func carrierFor(t *testing.T, c corpus.Corpus, r corpus.Record) TeamCarrier {
	t.Helper()
	carrier, err := NewTeamCarrier(queryOne(t, c, r))
	if err != nil {
		t.Fatalf("NewTeamCarrier: %v", err)
	}
	return carrier
}

// TestTeamCarrierEligibility asserts the three independent conditions the
// spec requires, each with its own row.
func TestTeamCarrierEligibility(t *testing.T) {
	cases := []struct {
		name     string
		corpus   corpus.Corpus
		record   corpus.Record
		eligible bool
		rule     string
	}{
		{"trusted team project record is eligible", teamCorpus(),
			recordIn(corpus.VisibilityTeam, corpus.PrivacyProject, corpus.TrustTrusted), true,
			"team-visible, trusted, non-personal is the shareable case"},
		{"private record is not team material", teamCorpus(),
			recordIn(corpus.VisibilityPrivate, corpus.PrivacyProject, corpus.TrustTrusted), false,
			"a private record is not team-visible"},
		{"scope-local record is not team material", teamCorpus(),
			recordIn(corpus.VisibilityScopeLocal, corpus.PrivacyProject, corpus.TrustTrusted), false,
			"scope-local reaches the chain, not the team"},
		{"shared record is not team material", teamCorpus(),
			recordIn(corpus.VisibilityShared, corpus.PrivacyProject, corpus.TrustTrusted), false,
			"shared crosses an edge; team is a distinct class"},
		{"untrusted record is never shareable", teamCorpus(),
			recordIn(corpus.VisibilityTeam, corpus.PrivacyProject, corpus.TrustUntrustedSource), false,
			"untrusted content is data, never something the user vouched for"},
		{"personal record is never shareable", teamCorpus(),
			recordIn(corpus.VisibilityTeam, corpus.PrivacyPersonal, corpus.TrustTrusted), false,
			"personal material is served only to a personal entitlement"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := carrierFor(t, tc.corpus, tc.record).EligibleForTeamShare()
			if tc.eligible && err != nil {
				t.Fatalf("EligibleForTeamShare() = %v, want eligible (%s)", err, tc.rule)
			}
			if !tc.eligible && err == nil {
				t.Fatalf("EligibleForTeamShare() = nil, want a refusal (%s)", tc.rule)
			}
		})
	}
}

// TestUntrustedTagPropagation is the S-10.T4 acceptance criterion carried
// into this ticket: a record whose OWN row claims trusted, inside a corpus
// classified untrusted-source, comes back untrusted and cannot be promoted
// to the team class. The record never gets to out-rank its corpus.
func TestUntrustedTagPropagation(t *testing.T) {
	untrusted := teamCorpus()
	untrusted.Trust = corpus.TrustUntrustedSource

	carrier := carrierFor(t, untrusted, recordIn(corpus.VisibilityTeam, corpus.PrivacyProject, corpus.TrustTrusted))

	if carrier.Trust() != corpus.TrustUntrustedSource {
		t.Fatalf("Trust() = %s, want %s: the corpus tag must propagate to the record",
			carrier.Trust(), corpus.TrustUntrustedSource)
	}
	if err := carrier.EligibleForTeamShare(); err == nil {
		t.Fatal("untrusted-tagged content was promoted to team-shared")
	}
	// And no grant can override it: a team-class grant still refuses.
	if _, err := carrier.ShareUnder(Decision{Granted: true, ScopeClass: corpus.VisibilityTeam}); err == nil {
		t.Fatal("a grant promoted untrusted-tagged content to team-shared")
	}
}

// TestGrantCannotWidenARecord is hard requirement 2. A grant may narrow
// reach; it may never raise a record above its own classification. The
// expectations are the spec's reach ordering (private < scope-local <
// shared < team), written out literally.
func TestGrantCannotWidenARecord(t *testing.T) {
	cases := []struct {
		record corpus.VisibilityClass
		grant  corpus.VisibilityClass
		want   corpus.VisibilityClass
	}{
		{corpus.VisibilityPrivate, corpus.VisibilityTeam, corpus.VisibilityPrivate},
		{corpus.VisibilityPrivate, corpus.VisibilityShared, corpus.VisibilityPrivate},
		{corpus.VisibilityScopeLocal, corpus.VisibilityTeam, corpus.VisibilityScopeLocal},
		{corpus.VisibilityShared, corpus.VisibilityTeam, corpus.VisibilityShared},
		{corpus.VisibilityTeam, corpus.VisibilityTeam, corpus.VisibilityTeam},
		{corpus.VisibilityTeam, corpus.VisibilityShared, corpus.VisibilityShared},
		{corpus.VisibilityTeam, corpus.VisibilityScopeLocal, corpus.VisibilityScopeLocal},
		{corpus.VisibilityTeam, corpus.VisibilityPrivate, corpus.VisibilityPrivate},
		// A grant whose class is unreadable narrows to the floor rather
		// than passing the record's own class through.
		{corpus.VisibilityTeam, corpus.VisibilityClass(""), corpus.VisibilityPrivate},
		{corpus.VisibilityTeam, corpus.VisibilityClass("everything"), corpus.VisibilityPrivate},
	}
	for _, tc := range cases {
		t.Run(string(tc.record)+"+grant:"+string(tc.grant), func(t *testing.T) {
			carrier := carrierFor(t, teamCorpus(), recordIn(tc.record, corpus.PrivacyProject, corpus.TrustTrusted))
			got := carrier.EffectiveClass(Grant{ScopeClass: tc.grant})
			if got != tc.want {
				t.Fatalf("EffectiveClass = %s, want %s (a grant must never widen a record)", got, tc.want)
			}
		})
	}
}

// TestNarrowerVisibilityIsSymmetric asserts the narrowing helper is order
// independent, so no caller can get a wider answer by swapping arguments.
func TestNarrowerVisibilityIsSymmetric(t *testing.T) {
	all := []corpus.VisibilityClass{
		corpus.VisibilityPrivate, corpus.VisibilityScopeLocal,
		corpus.VisibilityShared, corpus.VisibilityTeam,
		corpus.VisibilityClass(""), corpus.VisibilityClass("team-shared"),
	}
	for _, a := range all {
		for _, b := range all {
			if narrowerVisibility(a, b) != narrowerVisibility(b, a) {
				t.Fatalf("narrowerVisibility(%q,%q) is not symmetric", a, b)
			}
		}
	}
	if reachOf(corpus.VisibilityClass("unranked")) != 0 {
		t.Fatal("an unranked class must rank below private so drift fails closed")
	}
}

// TestShareUnderRequiresAGrantedDecision asserts the carrier refuses to
// share on the zero Decision, on a denied one, and when the grant narrows
// the record below the team class.
func TestShareUnderRequiresAGrantedDecision(t *testing.T) {
	carrier := carrierFor(t, teamCorpus(), recordIn(corpus.VisibilityTeam, corpus.PrivacyProject, corpus.TrustTrusted))

	if class, err := carrier.ShareUnder(Decision{}); err == nil {
		t.Fatalf("ShareUnder(zero Decision) = %s, nil; the zero value must deny", class)
	} else if class != corpus.VisibilityPrivate {
		t.Fatalf("a refused share returned class %s, want private", class)
	}
	if _, err := carrier.ShareUnder(Decision{Granted: false, ScopeClass: corpus.VisibilityTeam}); err == nil {
		t.Fatal("ShareUnder honoured a decision that was not granted")
	}
	if class, err := carrier.ShareUnder(Decision{Granted: true, ScopeClass: corpus.VisibilityShared}); err == nil {
		t.Fatalf("ShareUnder allowed a team share under a shared-class grant (got %s)", class)
	}
	class, err := carrier.ShareUnder(Decision{Granted: true, ScopeClass: corpus.VisibilityTeam})
	if err != nil {
		t.Fatalf("ShareUnder on the eligible case: %v", err)
	}
	if class != corpus.VisibilityTeam {
		t.Fatalf("ShareUnder = %s, want %s", class, corpus.VisibilityTeam)
	}
}

// TestNewTeamCarrierRefusesUnclassifiedRecords asserts a record that is not
// fully classified never becomes a carrier, and that a carrier built from a
// value whose fields were later blanked still reads fail-closed.
func TestNewTeamCarrierRefusesUnclassifiedRecords(t *testing.T) {
	cases := []struct {
		name string
		rec  corpus.Record
	}{
		{"zero value", corpus.Record{}},
		{"no visibility", corpus.Record{ID: "r", CorpusID: "c", ScopeRef: teamScope,
			Privacy: corpus.PrivacyProject, Trust: corpus.TrustTrusted}},
		{"no trust", corpus.Record{ID: "r", CorpusID: "c", ScopeRef: teamScope,
			Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityTeam}},
		{"no privacy", corpus.Record{ID: "r", CorpusID: "c", ScopeRef: teamScope,
			Visibility: corpus.VisibilityTeam, Trust: corpus.TrustTrusted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTeamCarrier(tc.rec); err == nil {
				t.Fatal("NewTeamCarrier accepted an unclassified record")
			}
		})
	}

	// A carrier holding a record whose axes are unreadable reports the
	// narrowest value on every axis rather than the stored garbage.
	blank := TeamCarrier{record: corpus.Record{ID: "r"}}
	if blank.Trust() != corpus.TrustUntrustedSource ||
		blank.Visibility() != corpus.VisibilityPrivate ||
		blank.Privacy() != corpus.PrivacyPersonal {
		t.Fatalf("an unreadable record read as %s/%s/%s, want untrusted/private/personal",
			blank.Trust(), blank.Visibility(), blank.Privacy())
	}
	if err := blank.EligibleForTeamShare(); err == nil {
		t.Fatal("an unreadable record was found team-shareable")
	}
	if got := carrierFor(t, teamCorpus(),
		recordIn(corpus.VisibilityTeam, corpus.PrivacyProject, corpus.TrustTrusted)).Record().ID; got != "r1" {
		t.Fatalf("Record() = %q, want the wrapped record", got)
	}
}
