// Purpose: R-21.95's limit-scope derivation. Every Bucket carries a stable
//
//	LimitScopeID (bucket.go); ResolveScope decides whether a QuotaDomain
//	gets its OWN scope or shares its account's scope, and DomainsInScope
//	answers "which domains does this scope cover".
//
// Inputs: an Account and a QuotaDomain. Outputs: a LimitScopeID.
// Constraints: sharing the account-level scope is the FAIL-CLOSED default
//
//	-- a distinct per-domain scope is granted only to an api_project
//	domain that explicitly proves provider-documented independence
//	(QuotaDomain.IndependenceProven); an unproven claim can never split a
//	real provider limit.
//
// SPORT: fleet/topology/limit_scope/ADD (P1-E40-W9-S77-T3).

package topology

// AccountScope returns the shared, account-level LimitScopeID every
// QuotaDomain falls back to unless ResolveScope grants a distinct one.
func AccountScope(account AccountID) LimitScopeID {
	return LimitScopeID("scope:" + string(account))
}

// domainScope returns the distinct per-domain LimitScopeID a proven-
// independent api_project domain gets.
func domainScope(account AccountID, domain DomainID) LimitScopeID {
	return LimitScopeID("scope:" + string(account) + ":" + string(domain))
}

// ResolveScope derives d's LimitScopeID. A distinct scope:<account>:<domain>
// is granted only when d.Kind is api_project AND d.IndependenceProven is
// true -- the provider's documented per-project semantics being the one
// case R-21.95 recognises as genuinely independent. Every other kind, and
// any api_project domain that has not proven independence, conservatively
// shares the account-level scope.
func ResolveScope(account Account, d QuotaDomain) LimitScopeID {
	if d.Kind == QuotaDomainAPIProject && d.IndependenceProven {
		return domainScope(account.ID, d.ID)
	}
	return AccountScope(account.ID)
}

// DomainsInScope lists every domain in domains whose ResolveScope (given
// its owning account in accounts) equals scope. A domain whose account is
// missing from accounts is skipped rather than guessed.
func DomainsInScope(scope LimitScopeID, domains []QuotaDomain, accounts map[AccountID]Account) []DomainID {
	var out []DomainID
	for _, d := range domains {
		acct, ok := accounts[d.AccountRef]
		if !ok {
			continue
		}
		if ResolveScope(acct, d) == scope {
			out = append(out, d.ID)
		}
	}
	return out
}
