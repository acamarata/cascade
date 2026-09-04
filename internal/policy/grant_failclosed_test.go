// Purpose: hard requirement 1 for the grant model, one explicit case per
// rule: an unknown capability, an unparseable grant, a missing subject, an
// expired grant and a malformed scope each DENY, and none degrades into a
// match-all. A live authorization bypass of exactly this shape has shipped
// in this repo before — a helper that returned "allowed" for input it could
// not decode — so every one of these is asserted directly rather than
// assumed from the code's shape.
//
// Split from grant_test.go as a sibling file per R-14.117 (Art.10.3's
// 300-line cap).
//
// SPORT: internal/policy StoreGrants.Check/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/storage"
)

// denies runs a Check and asserts it denied, with the zero Decision.
func denies(t *testing.T, f *grantFixture, req CheckRequest, rule string) error {
	t.Helper()
	d, err := f.store.Check(context.Background(), req)
	if err == nil {
		t.Fatalf("Check allowed %+v; %s", req, rule)
	}
	if d.Granted {
		t.Fatalf("a denied Check returned Granted=true; %s", rule)
	}
	if d.ScopeClass.Valid() {
		t.Fatalf("a denied Check returned a usable scope class %s; %s", d.ScopeClass, rule)
	}
	return err
}

// TestCheckDeniesUnknownCapability asserts an unregistered capability
// denies, and keeps denying after the capability is removed from the
// registry while its grant row remains on disk.
func TestCheckDeniesUnknownCapability(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)

	err := denies(t, f, CheckRequest{Subject: testSubject(), Capability: "never.registered"},
		"an unregistered capability must deny")
	if !strings.Contains(err.Error(), CodeCapabilityNotFound) {
		t.Fatalf("refusal %q does not carry the %q code", err, CodeCapabilityNotFound)
	}

	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := f.reg.Remove(ctx, readCap().Name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_ = denies(t, f, CheckRequest{Subject: testSubject(), Capability: readCap().Name},
		"a grant on a de-registered capability must deny")
}

// TestGrantWriteDeniesUnknownCapability asserts a grant naming an
// unregistered capability is refused at WRITE time too, so the row never
// exists to be argued about later.
func TestGrantWriteDeniesUnknownCapability(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	g := validGrant()
	g.Capability = "never.registered"
	if err := f.store.Grant(ctx, g); err == nil {
		t.Fatal("Grant accepted an unregistered capability")
	}
}

// TestCheckDeniesBadSubject asserts every malformed or missing subject
// denies with subject-unknown, and that no subject value is a wildcard.
func TestCheckDeniesBadSubject(t *testing.T) {
	f := newGrantFixture(t)
	if err := f.store.Grant(context.Background(), validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	cases := []struct {
		name string
		subj Subject
	}{
		{"zero value", Subject{}},
		{"no kind", Subject{ID: "lane-a"}},
		{"unknown kind", Subject{Kind: SubjectKind("root"), ID: "lane-a"}},
		{"no id", Subject{Kind: SubjectAgent}},
		{"wildcard id", Subject{Kind: SubjectAgent, ID: "*"}},
		{"path separator in id", Subject{Kind: SubjectAgent, ID: "lane/a"}},
		{"control char in id", Subject{Kind: SubjectAgent, ID: "lane\na"}},
		{"over-length id", Subject{Kind: SubjectAgent, ID: strings.Repeat("x", maxSubjectIDLen+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := denies(t, f, CheckRequest{Subject: tc.subj, Capability: readCap().Name},
				"a subject that names nobody must deny")
			if !strings.Contains(err.Error(), CodeSubjectUnknown) {
				t.Fatalf("refusal %q does not carry the %q code", err, CodeSubjectUnknown)
			}
		})
	}
	// A different, well-formed subject holds nothing: a grant is not a
	// grant to everybody.
	_ = denies(t, f, CheckRequest{Subject: Subject{Kind: SubjectAgent, ID: "lane-b"}, Capability: readCap().Name},
		"another subject must not inherit this subject's grant")
	_ = denies(t, f, CheckRequest{Subject: Subject{Kind: SubjectUser, ID: "lane-a"}, Capability: readCap().Name},
		"the same id under a different kind is a different subject")
}

// writeRaw puts bytes directly into the `policy` domain namespace under a
// grant key, bypassing StoreGrants entirely — the only way to model a row
// that was corrupted, hand-edited, or written by an older build.
func writeRaw(t *testing.T, f *grantFixture, key string, value []byte) {
	t.Helper()
	if err := f.db.Put(context.Background(), string(storage.DomainPolicy), key, value); err != nil {
		t.Fatalf("seeding raw row: %v", err)
	}
}

// TestCheckDeniesUnparseableGrant asserts an undecodable or invalid stored
// row denies. This is the exact shape of the bypass that shipped before.
func TestCheckDeniesUnparseableGrant(t *testing.T) {
	key := validGrant().key()
	cases := []struct {
		name string
		raw  []byte
	}{
		{"not json", []byte("this is not json")},
		{"empty", []byte{}},
		{"json null", []byte("null")},
		{"json array", []byte("[]")},
		{"empty object", []byte("{}")},
		{"truncated", []byte(`{"subject":{"kind":"agent"`)},
		{"wrong field types", []byte(`{"subject":"lane-a","capability":42}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGrantFixture(t)
			writeRaw(t, f, key, tc.raw)
			err := denies(t, f, CheckRequest{Subject: testSubject(), Capability: readCap().Name},
				"an unreadable grant row must deny")
			if !strings.Contains(err.Error(), CodeGrantDenied) {
				t.Fatalf("refusal %q does not carry the %q code", err, CodeGrantDenied)
			}
		})
	}
}

// TestCheckDeniesMalformedScope asserts a stored grant whose scope class
// is absent, unknown or a wildcard denies rather than being read as any
// reach at all.
func TestCheckDeniesMalformedScope(t *testing.T) {
	for _, class := range []string{"", "everything", "*", "TEAM", "team-shared"} {
		t.Run("scope="+class, func(t *testing.T) {
			f := newGrantFixture(t)
			g := validGrant()
			g.ScopeClass = corpus.VisibilityClass(class)
			raw, err := json.Marshal(g)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			writeRaw(t, f, g.key(), raw)
			_ = denies(t, f, CheckRequest{Subject: testSubject(), Capability: readCap().Name},
				"a malformed scope class must deny")
			// The same value is refused at write time as well.
			if err := f.store.Grant(context.Background(), g); err == nil {
				t.Fatal("Grant accepted a malformed scope class")
			}
		})
	}
}

// TestCheckDeniesForgedKey asserts a grant row parked under another
// subject's or another capability's key does not authorise that key. The
// key is not trusted to describe its own contents.
func TestCheckDeniesForgedKey(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	if err := f.reg.Add(ctx, Capability{Name: "memory.write", Desc: "d", DefaultPolicy: ClassWorkspaceMutation}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	g := validGrant()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The row says memory.read for lane-a; park it under lane-b's key and
	// under lane-a's memory.write key.
	writeRaw(t, f, Grant{Subject: Subject{Kind: SubjectAgent, ID: "lane-b"}, Capability: readCap().Name}.key(), raw)
	writeRaw(t, f, Grant{Subject: testSubject(), Capability: "memory.write"}.key(), raw)

	_ = denies(t, f, CheckRequest{Subject: Subject{Kind: SubjectAgent, ID: "lane-b"}, Capability: readCap().Name},
		"a row filed under another subject's key must not authorise that subject")
	_ = denies(t, f, CheckRequest{Subject: testSubject(), Capability: "memory.write"},
		"a row filed under another capability's key must not authorise that capability")
}

// TestListFailsClosedOnCorruptRow asserts List reports a corrupt row
// rather than quietly omitting it, so an operator reviewing what a subject
// holds sees the whole truth or an error, never a silently short list.
func TestListFailsClosedOnCorruptRow(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	writeRaw(t, f, grantKeyPrefix+"agent/lane-a/memory.write", []byte("not json"))
	if _, err := f.store.List(ctx, testSubject()); err == nil {
		t.Fatal("List hid a corrupt row instead of reporting it")
	}
	if _, err := f.store.List(ctx, Subject{}); err == nil {
		t.Fatal("List accepted an empty subject")
	}
}

// TestGrantValidateFailsClosed covers Grant.Validate's own refusals
// without a store, so each rule is visible on its own.
func TestGrantValidateFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		g    Grant
	}{
		{"zero value", Grant{}},
		{"no subject", Grant{Capability: "a.b", ScopeClass: corpus.VisibilityTeam}},
		{"no capability", Grant{Subject: testSubject(), ScopeClass: corpus.VisibilityTeam}},
		{"malformed capability", Grant{Subject: testSubject(), Capability: "a b", ScopeClass: corpus.VisibilityTeam}},
		{"no scope class", Grant{Subject: testSubject(), Capability: "a.b"}},
		{"empty condition key", Grant{Subject: testSubject(), Capability: "a.b",
			ScopeClass: corpus.VisibilityTeam, Conditions: map[string]string{"": "v"}}},
		{"empty condition value", Grant{Subject: testSubject(), Capability: "a.b",
			ScopeClass: corpus.VisibilityTeam, Conditions: map[string]string{"k": " "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.g.Validate(); err == nil {
				t.Fatal("Validate accepted a grant that cannot be evaluated")
			}
		})
	}
	if err := validGrant().Validate(); err != nil {
		t.Fatalf("Validate rejected a well-formed grant: %v", err)
	}
	if SubjectKind("root").String() != "invalid" {
		t.Fatal("an undefined subject kind must not render as a real spelling")
	}
}
