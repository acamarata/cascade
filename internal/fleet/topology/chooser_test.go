package topology

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func baseTopology() (Account, QuotaDomain, Lane, Credential) {
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	dom := QuotaDomain{
		ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid,
		Dimensions: map[string]Bucket{
			DimensionRPM: {Name: DimensionRPM, Limit: 1000, Source: SourceCLIObservation, RemainingFraction: 1},
		},
	}
	lane := Lane{ID: "lane-1", QuotaDomainRef: "d1", ModelID: "model-x"}
	cred := Credential{ID: "cred-1", AccountRef: "a1", QuotaDomainRef: "d1", RuntimeProfileRef: "rp1", Health: CredentialOK}
	return acct, dom, lane, cred
}

// TestChooserEnumeratesCandidatesOnly asserts R-21.102: Candidates returns
// every eligible pair with its signals and computes no argmin, and Choose
// makes the ONE residual decision (a credential inside the already-chosen
// domain), never a second selection across domains.
func TestChooserEnumeratesCandidatesOnly(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, admitAllCreds, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	cands, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x", Estimates: map[string]int64{DimensionRPM: 10}})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Lane.ID != "lane-1" || cands[0].Domain.ID != "d1" {
		t.Fatalf("unexpected candidates: %+v", cands)
	}

	chosen, err := c.Choose(context.Background(), lane, "internal")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if chosen != "cred-1" {
		t.Fatalf("Choose = %v, want cred-1", chosen)
	}
}

// TestChoose429ConstrainsScopeNotDomain -- see chooser_errors_test.go for
// the full 429/scope test; this file only covers Choose/Candidates
// plumbing so eligibility failures are exercised close to their source.
func TestCandidatesFailsClosedOnUnconfiguredPrivacyGate(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, nil, admitAllCreds, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	_, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x"})
	if err == nil {
		t.Fatal("expected Candidates to fail closed with no privacy gate configured")
	}
}

// TestChooseFailsClosedWithNoHealthyCredential asserts Choose never falls
// back to "the first one" when nothing in the domain is healthy.
func TestChooseFailsClosedWithNoHealthyCredential(t *testing.T) {
	_, dom, lane, cred := baseTopology()
	cred.Health = CredentialQuarantined
	c := NewChooser(newTestClock(), 1, alwaysAllow, admitAllCreds, nil)
	c.SetTopology(nil, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	if _, err := c.Choose(context.Background(), lane, "internal"); err == nil {
		t.Fatal("expected Choose to fail closed when no credential is healthy")
	}
}

// TestChooseRoundRobinsDeterministically asserts Choose round-robins among
// health==ok credentials in ascending-ID order, never randomly.
func TestChooseRoundRobinsDeterministically(t *testing.T) {
	_, dom, lane, _ := baseTopology()
	c1 := Credential{ID: "cred-a", QuotaDomainRef: "d1", RuntimeProfileRef: "rp1", Health: CredentialOK}
	c2 := Credential{ID: "cred-b", QuotaDomainRef: "d1", RuntimeProfileRef: "rp1", Health: CredentialOK}
	c := NewChooser(newTestClock(), 1, alwaysAllow, admitAllCreds, nil)
	c.SetTopology(nil, []QuotaDomain{dom}, []Lane{lane}, []Credential{c1, c2})

	first, err := c.Choose(context.Background(), lane, "internal")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	second, err := c.Choose(context.Background(), lane, "internal")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	third, err := c.Choose(context.Background(), lane, "internal")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if first != "cred-a" || second != "cred-b" || third != "cred-a" {
		t.Fatalf("round robin order = %v, %v, %v; want cred-a, cred-b, cred-a", first, second, third)
	}
}

// evidenceFixture is a paid domain with per-credential stored rows and
// current keys, read through the production EvidenceCredentialGate.
type evidenceFixture struct {
	stored     map[CredentialID]EvidenceKey
	current    map[CredentialID]EvidenceKey
	storedErr  error
	currentErr error
}

func (f *evidenceFixture) gate() CredentialGate {
	return EvidenceCredentialGate(
		func(_ context.Context, id CredentialID) (EvidenceKey, bool, error) {
			k, ok := f.stored[id]
			return k, ok, f.storedErr
		},
		func(_ context.Context, cr Credential) (EvidenceKey, error) { return f.current[cr.ID], f.currentErr })
}

func wantRefusal(t *testing.T, err, sentinel error, msg string) {
	t.Helper()
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v; want identity %v and message %q", err, sentinel, msg)
	}
}

// TestPaidDomainWithoutProviderEvidenceCapsPublic: a second provider
// enrolled into a paid domain reads public until its own promote while the
// first stays internal; Choose never picks it for an internal request; an
// evidence read error refuses.
func TestPaidDomainWithoutProviderEvidenceCapsPublic(t *testing.T) {
	ctx := context.Background()
	_, dom, lane, _ := baseTopology()
	a := Credential{ID: "cred-a", QuotaDomainRef: "d1", Health: CredentialOK}
	b := Credential{ID: "cred-b", QuotaDomainRef: "d1", Health: CredentialOK}
	ka, kb := EvidenceKey{Endpoint: "https://a", InstanceID: "i1", AuthRef: "ra", CredentialFingerprint: "fa"}, EvidenceKey{Endpoint: "https://b", InstanceID: "i2", AuthRef: "rb", CredentialFingerprint: "fb"}
	f := &evidenceFixture{stored: map[CredentialID]EvidenceKey{"cred-a": ka}, current: map[CredentialID]EvidenceKey{"cred-a": ka, "cred-b": kb}}
	gate := f.gate()
	if c, err := gate(ctx, a, dom); err != nil || c.Tier != provider.SensitivityInternal {
		t.Fatalf("promoted provider = %+v, %v; want internal", c, err)
	}
	if c, err := gate(ctx, b, dom); err != nil || c.Tier != provider.SensitivityPublic || c.Reason != CeilingBillingCap {
		t.Fatalf("unpromoted provider = %+v, %v; want public/billing-cap", c, err)
	}
	ch := NewChooser(newTestClock(), 1, BillingPrivacyGate, gate, nil)
	ch.SetTopology(nil, []QuotaDomain{dom}, []Lane{lane}, []Credential{a, b})
	for i := 0; i < 4; i++ {
		if got, err := ch.Choose(ctx, lane, "internal"); err != nil || got != "cred-a" {
			t.Fatalf("internal pick %d = %v, %v; want cred-a only", i, got, err)
		}
	}
	seen := map[CredentialID]bool{}
	for i := 0; i < 2; i++ {
		got, err := ch.Choose(ctx, lane, "public")
		if err != nil {
			t.Fatalf("public pick: %v", err)
		}
		seen[got] = true
	}
	if !seen["cred-b"] {
		t.Fatalf("public picks %v never used cred-b", seen)
	}
	f.stored["cred-b"] = kb
	seen = map[CredentialID]bool{}
	for i := 0; i < 2; i++ {
		got, _ := ch.Choose(ctx, lane, "internal")
		seen[got] = true
	}
	if !seen["cred-a"] || !seen["cred-b"] {
		t.Fatalf("after its own promote internal picks = %v; want both", seen)
	}
	boom := errors.New("evidence store down")
	f.storedErr = boom
	got, err := ch.Choose(ctx, lane, "internal")
	if got != "" {
		t.Fatalf("read error picked %v", got)
	}
	wantRefusal(t, err, boom, "billing evidence for credential")
}

// TestReaddedProviderDoesNotInheritEvidence: a current key differing by
// any field reads public/billing-cap; a key fn error is ineligible; an
// unchanged key stays internal.
func TestReaddedProviderDoesNotInheritEvidence(t *testing.T) {
	ctx := context.Background()
	_, dom, lane, _ := baseTopology()
	cr := Credential{ID: "cred-a", QuotaDomainRef: "d1", Health: CredentialOK}
	base := EvidenceKey{Endpoint: "https://a", InstanceID: "i1", AuthRef: "ra", CredentialFingerprint: "fa"}
	changes := map[string]func(*EvidenceKey){
		"endpoint":               func(k *EvidenceKey) { k.Endpoint = "https://other" },
		"instance-id":            func(k *EvidenceKey) { k.InstanceID = "i2" },
		"auth-ref":               func(k *EvidenceKey) { k.AuthRef = "rb" },
		"credential-fingerprint": func(k *EvidenceKey) { k.CredentialFingerprint = "fb" },
	}
	for name, change := range changes {
		cur := base
		change(&cur)
		f := &evidenceFixture{stored: map[CredentialID]EvidenceKey{"cred-a": base}, current: map[CredentialID]EvidenceKey{"cred-a": cur}}
		if c, err := f.gate()(ctx, cr, dom); err != nil || c.Tier != provider.SensitivityPublic || c.Reason != CeilingBillingCap {
			t.Fatalf("%s changed = %+v, %v; want public/billing-cap", name, c, err)
		}
	}
	f := &evidenceFixture{stored: map[CredentialID]EvidenceKey{"cred-a": base}, current: map[CredentialID]EvidenceKey{"cred-a": base}}
	if c, err := f.gate()(ctx, cr, dom); err != nil || c.Tier != provider.SensitivityInternal {
		t.Fatalf("unchanged = %+v, %v; want internal", c, err)
	}
	boom := errors.New("key fn failed")
	f.currentErr = boom
	_, err := f.gate()(ctx, cr, dom)
	wantRefusal(t, err, boom, "current evidence key for credential")
	ch := NewChooser(newTestClock(), 1, BillingPrivacyGate, f.gate(), nil)
	ch.SetTopology(nil, []QuotaDomain{dom}, []Lane{lane}, []Credential{cr})
	_, err = ch.Choose(ctx, lane, "public")
	wantRefusal(t, err, boom, "current evidence key for credential")
}

// TestChooserNilCredentialGateRefusesEveryCredential: no gate, no pick.
func TestChooserNilCredentialGateRefusesEveryCredential(t *testing.T) {
	_, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil, nil)
	c.SetTopology(nil, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})
	for _, class := range []string{"public", "internal", ""} {
		if got, err := c.Choose(context.Background(), lane, class); err == nil || got != "" {
			t.Fatalf("nil gate class %q picked %v, %v", class, got, err)
		}
	}
}

// TestChooserBillingPrivacyGate: free and discover admit public only, paid
// any tier; a bad class or billing tier refuses.
func TestChooserBillingPrivacyGate(t *testing.T) {
	for _, tc := range []struct {
		tier  BillingTier
		class string
		want  bool
	}{
		{BillingTierFree, "public", true}, {BillingTierFree, "internal", false}, {BillingTierDiscover, "", false},
		{BillingTierDiscover, "public", true}, {BillingTierPaid, "local-only", true}, {BillingTierPaid, "internal", true},
		{BillingTierPaid, "secret", false}, {BillingTier("gold"), "public", false},
	} {
		if got := BillingPrivacyGate(QuotaDomain{BillingTier: tc.tier}, tc.class); got != tc.want {
			t.Fatalf("BillingPrivacyGate(%s, %q) = %v, want %v", tc.tier, tc.class, got, tc.want)
		}
	}
}
