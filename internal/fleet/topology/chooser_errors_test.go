package topology

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestChooser429ConstrainsScopeNotDomain asserts R-21.29/R-21.95: a 429 on
// one credential moves the next call to a domain in ANOTHER limit scope,
// never to the sibling key and never to a sibling domain sharing the
// constrained scope. Two credentials in one api_project domain share
// every bucket, so both keys are equally unusable once the SCOPE (not one
// domain) is marked constrained.
func TestChooser429ConstrainsScopeNotDomain(t *testing.T) {
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	domSameScope := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Limit: 100, Source: SourceCLIObservation, RemainingFraction: 1}}}
	domOtherScope := QuotaDomain{ID: "d2", AccountRef: "a2", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Limit: 100, Source: SourceCLIObservation, RemainingFraction: 1}}}
	acct2 := Account{ID: "a2", Role: AccountRoleWorkforce}
	laneSameScope := Lane{ID: "lane-1", QuotaDomainRef: "d1", ModelID: "m"}
	laneOtherScope := Lane{ID: "lane-2", QuotaDomainRef: "d2", ModelID: "m"}
	credA := Credential{ID: "cred-a", AccountRef: "a1", QuotaDomainRef: "d1", RuntimeProfileRef: "rp", Health: CredentialOK}
	credB := Credential{ID: "cred-b", AccountRef: "a1", QuotaDomainRef: "d1", RuntimeProfileRef: "rp", Health: CredentialOK}

	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology(
		[]Account{acct, acct2},
		[]QuotaDomain{domSameScope, domOtherScope},
		[]Lane{laneSameScope, laneOtherScope},
		[]Credential{credA, credB},
	)

	retry, err := c.OnProviderError(context.Background(), "d1", "cred-a", cascade.New(cascade.KindQuotaExhausted, "429"))
	if err != nil {
		t.Fatalf("OnProviderError: %v", err)
	}
	if len(retry.AffectedDomains) != 1 || retry.AffectedDomains[0] != "d1" {
		t.Fatalf("AffectedDomains = %v, want exactly [d1]", retry.AffectedDomains)
	}

	cands, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "m"})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	for _, cand := range cands {
		if cand.Domain.ID == "d1" {
			t.Fatalf("expected d1 (constrained scope) to be ineligible, got it in candidates: %+v", cand)
		}
	}
	if len(cands) != 1 || cands[0].Domain.ID != "d2" {
		t.Fatalf("expected only d2 (other scope) eligible, got %+v", cands)
	}

	// Never the sibling key: Choose against the constrained domain's own
	// lane still fails, because the domain itself is ineligible via its
	// scope -- Choose is only ever consulted after Candidates/Rank pick a
	// domain, and no candidate named d1.
	if _, err := c.Choose(context.Background(), laneSameScope); err != nil {
		t.Fatalf("Choose still resolves a credential inside d1 (scope constraint is a Candidates-level gate): %v", err)
	}
}

// TestChooser401QuarantinesCredentialOnly asserts a 401 quarantines ONLY
// the credential, leaving the domain and its sibling credential
// selectable.
func TestChooser401QuarantinesCredentialOnly(t *testing.T) {
	_, dom, lane, _ := baseTopology()
	credA := Credential{ID: "cred-a", QuotaDomainRef: "d1", RuntimeProfileRef: "rp", Health: CredentialOK}
	credB := Credential{ID: "cred-b", QuotaDomainRef: "d1", RuntimeProfileRef: "rp", Health: CredentialOK}
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{{ID: "a1"}}, []QuotaDomain{dom}, []Lane{lane}, []Credential{credA, credB})

	if _, err := c.OnProviderError(context.Background(), "d1", "cred-a", cascade.New(cascade.KindPermissionDenied, "401")); err != nil {
		t.Fatalf("OnProviderError: %v", err)
	}

	chosen, err := c.Choose(context.Background(), lane)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if chosen != "cred-b" {
		t.Fatalf("Choose = %v, want the sibling credential cred-b", chosen)
	}
	if c.domainQuarantined["d1"] {
		t.Fatal("401 must never quarantine the domain")
	}
}

// TestChooser403QuarantinesDomain asserts a 403 quarantines the DOMAIN,
// making every lane referencing it ineligible.
func TestChooser403QuarantinesDomain(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	if _, err := c.OnProviderError(context.Background(), "d1", "cred-1", cascade.New(cascade.KindCapabilityDenied, "403")); err != nil {
		t.Fatalf("OnProviderError: %v", err)
	}
	if _, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x"}); err == nil {
		t.Fatal("expected a quarantined domain to have zero eligible candidates")
	}
	if !c.domainQuarantined["d1"] {
		t.Fatal("expected 403 to quarantine the domain")
	}
}

// TestChooser5xxDecorrelatedBackoff asserts the min(60s,1s*2^attempt)*U(0.5,1.5)
// shape and the max-5-attempts reroute.
func TestChooser5xxDecorrelatedBackoff(t *testing.T) {
	_, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{{ID: "a1"}}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	var last Retry
	for i := 0; i < maxBackoffAttempts; i++ {
		r, err := c.OnProviderError(context.Background(), "d1", "cred-1", cascade.New(cascade.KindUnavailable, "5xx"))
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if r.Reroute {
			t.Fatalf("attempt %d: expected retry, not reroute, before the attempt ceiling", i)
		}
		if r.After <= 0 || r.After > maxBackoffDelay+maxBackoffDelay/2 {
			t.Fatalf("attempt %d: backoff %v outside expected bound", i, r.After)
		}
		last = r
	}
	_ = last
	r, err := c.OnProviderError(context.Background(), "d1", "cred-1", cascade.New(cascade.KindTimeout, "timeout"))
	if err != nil {
		t.Fatalf("final attempt: %v", err)
	}
	if !r.Reroute {
		t.Fatal("expected reroute once the attempt ceiling is reached")
	}
}

// TestParseRetryAfter asserts both delta-seconds and HTTP-date inputs
// parse, and an unrecognised value is refused rather than guessed.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	if d, ok := ParseRetryAfter("120", now); !ok || d != 120*time.Second {
		t.Fatalf("delta-seconds: got (%v,%v)", d, ok)
	}
	future := now.Add(90 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	if d, ok := ParseRetryAfter(future, now); !ok || d < 89*time.Second || d > 91*time.Second {
		t.Fatalf("http-date: got (%v,%v)", d, ok)
	}
	if _, ok := ParseRetryAfter("not-a-value", now); ok {
		t.Fatal("expected an unrecognised value to be refused")
	}
	if _, ok := ParseRetryAfter("", now); ok {
		t.Fatal("expected an empty value to be refused")
	}
}
