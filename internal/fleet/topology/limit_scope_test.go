package topology

import "testing"

// TestResolveScopeSharesAccountScopeWhenUnproven is the named acceptance
// test: an api_project domain that has not proven independence shares the
// account-level scope, and every non-api_project kind always shares it
// too -- the fail-closed default R-21.95 requires.
func TestResolveScopeSharesAccountScopeWhenUnproven(t *testing.T) {
	acct := Account{ID: "acct-1"}
	unproven := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject}
	if got, want := ResolveScope(acct, unproven), AccountScope("acct-1"); got != want {
		t.Errorf("unproven api_project scope = %q, want shared account scope %q", got, want)
	}

	subscription := QuotaDomain{ID: "dom-2", AccountRef: "acct-1", Kind: QuotaDomainSubscriptionWindow, IndependenceProven: true}
	if got, want := ResolveScope(acct, subscription), AccountScope("acct-1"); got != want {
		t.Errorf("non-api_project kind must always share the account scope, got %q want %q", got, want)
	}
}

func TestResolveScopeGrantsDistinctScopeWhenProven(t *testing.T) {
	acct := Account{ID: "acct-1"}
	proven := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject, IndependenceProven: true}
	got := ResolveScope(acct, proven)
	want := domainScope("acct-1", "dom-1")
	if got != want {
		t.Errorf("proven api_project scope = %q, want distinct scope %q", got, want)
	}
	if got == AccountScope("acct-1") {
		t.Error("a proven-independent domain must not collapse to the shared account scope")
	}
}

func TestDomainsInScope(t *testing.T) {
	accounts := map[AccountID]Account{"acct-1": {ID: "acct-1"}, "acct-2": {ID: "acct-2"}}
	domains := []QuotaDomain{
		{ID: "d1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject, IndependenceProven: true},
		{ID: "d2", AccountRef: "acct-1", Kind: QuotaDomainSharedPool},
		{ID: "d3", AccountRef: "acct-2", Kind: QuotaDomainSharedPool},
	}
	shared := DomainsInScope(AccountScope("acct-1"), domains, accounts)
	if len(shared) != 1 || shared[0] != "d2" {
		t.Errorf("DomainsInScope(account scope) = %v, want [d2]", shared)
	}
	distinct := DomainsInScope(domainScope("acct-1", "d1"), domains, accounts)
	if len(distinct) != 1 || distinct[0] != "d1" {
		t.Errorf("DomainsInScope(distinct scope) = %v, want [d1]", distinct)
	}
}

func TestDomainsInScopeSkipsMissingAccount(t *testing.T) {
	domains := []QuotaDomain{{ID: "orphan", AccountRef: "ghost", Kind: QuotaDomainSharedPool}}
	got := DomainsInScope(AccountScope("ghost"), domains, map[AccountID]Account{})
	if len(got) != 0 {
		t.Errorf("DomainsInScope with a missing account should skip it, got %v", got)
	}
}
