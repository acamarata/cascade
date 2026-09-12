package topology

import (
	"context"
	"testing"
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
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	cands, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x", Estimates: map[string]int64{DimensionRPM: 10}})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Lane.ID != "lane-1" || cands[0].Domain.ID != "d1" {
		t.Fatalf("unexpected candidates: %+v", cands)
	}

	chosen, err := c.Choose(context.Background(), lane)
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
	c := NewChooser(newTestClock(), 1, nil, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	_, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x"})
	if err == nil {
		t.Fatal("expected Candidates to fail closed with no privacy gate configured")
	}
}

// TestChooseFailsClosedWithNoHealthyCredential asserts Choose never falls
// back to "the first one" when nothing in the domain is healthy.
func TestChooseFailsClosedWithNoHealthyCredential(t *testing.T) {
	_, _, lane, cred := baseTopology()
	cred.Health = CredentialQuarantined
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology(nil, nil, []Lane{lane}, []Credential{cred})

	if _, err := c.Choose(context.Background(), lane); err == nil {
		t.Fatal("expected Choose to fail closed when no credential is healthy")
	}
}

// TestChooseRoundRobinsDeterministically asserts Choose round-robins among
// health==ok credentials in ascending-ID order, never randomly.
func TestChooseRoundRobinsDeterministically(t *testing.T) {
	_, _, lane, _ := baseTopology()
	c1 := Credential{ID: "cred-a", QuotaDomainRef: "d1", RuntimeProfileRef: "rp1", Health: CredentialOK}
	c2 := Credential{ID: "cred-b", QuotaDomainRef: "d1", RuntimeProfileRef: "rp1", Health: CredentialOK}
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology(nil, nil, []Lane{lane}, []Credential{c1, c2})

	first, err := c.Choose(context.Background(), lane)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	second, err := c.Choose(context.Background(), lane)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	third, err := c.Choose(context.Background(), lane)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if first != "cred-a" || second != "cred-b" || third != "cred-a" {
		t.Fatalf("round robin order = %v, %v, %v; want cred-a, cred-b, cred-a", first, second, third)
	}
}
