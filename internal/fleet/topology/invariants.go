// Purpose: Validate(ctx, Snapshot) []Violation, the R-21.24 invariant
//
//	checker plus the R-21.107 credential-domain-profile join invariant.
//	An offending lane is written with health quarantined and its
//	violation reported -- never silently dropped, never repaired by
//	guessing a reference.
//
// Inputs: a Snapshot -- the full or partial set of topology rows a caller
//
//	(store.go's per-write check, reconcile.go's pre-commit check, or a
//	test) wants checked together.
//
// Outputs: []Violation, empty when s satisfies every invariant.
// Constraints: quarantine propagation and the join invariant never repair
//
//	a row -- they only report; the caller decides what to persist.
//
// SPORT: fleet/topology/invariants/ADD (P1-E40-W9-S77-T1).

package topology

import "context"

// BucketOwnerKind is the closed vocabulary BucketRow.Owner draws from --
// the R-21.24 rule that bucket ownership is per QUOTA DOMAIN, never per
// credential, even though the full bucket type (R-21.26) does not land
// until S-77.T3. BucketRow exists now, ahead of that ticket, purely so
// this invariant is real and testable in this ticket rather than a stub
// pending a future field.
type BucketOwnerKind string

// The two closed BucketOwnerKind members. Only BucketOwnerQuotaDomain is
// ever legal; BucketOwnerCredential exists so the violation is
// representable and testable.
const (
	BucketOwnerQuotaDomain BucketOwnerKind = "quota_domain"
	BucketOwnerCredential  BucketOwnerKind = "credential"
)

// BucketRow is a minimal forward declaration of one capacity-bucket
// ownership assertion, ahead of R-21.26's full bucket type (S-77.T3). It
// exists only to make the R-21.24 "bucket ownership is per domain, never
// per credential" invariant checkable now.
type BucketRow struct {
	Owner   BucketOwnerKind
	OwnerID string
}

// Snapshot is the set of topology rows one Validate call checks together.
// A caller may pass every row in the store (Reconcile's pre-commit check)
// or just the rows relevant to one write (store.go).
type Snapshot struct {
	Accounts        []Account
	Credentials     []Credential
	QuotaDomains    []QuotaDomain
	RuntimeProfiles []RuntimeProfile
	Lanes           []Lane
	Buckets         []BucketRow
}

// Validate checks s against every R-21.24 invariant plus the R-21.107
// join invariant, returning one Violation per offense. ctx is accepted for
// forward-compatibility with a future invariant needing external state; it
// is not read by any check in this ticket.
func Validate(_ context.Context, s Snapshot) []Violation {
	var out []Violation
	domainByID := indexQuotaDomains(s.QuotaDomains)
	credByID := indexCredentials(s.Credentials)
	profileByID := indexRuntimeProfiles(s.RuntimeProfiles)

	out = append(out, validateCredentialDomains(s.Credentials, domainByID)...)
	out = append(out, validateLaneReferences(s.Lanes, domainByID, profileByID)...)
	out = append(out, validateBucketOwnership(s.Buckets)...)
	out = append(out, validateQuarantinePropagation(s.Lanes, domainByID)...)
	out = append(out, validateCredentialJoin(s.Lanes, credByID, domainByID, profileByID)...)
	return out
}

func indexQuotaDomains(domains []QuotaDomain) map[DomainID]QuotaDomain {
	m := make(map[DomainID]QuotaDomain, len(domains))
	for _, d := range domains {
		m[d.ID] = d
	}
	return m
}

func indexCredentials(creds []Credential) map[CredentialID]Credential {
	m := make(map[CredentialID]Credential, len(creds))
	for _, c := range creds {
		m[c.ID] = c
	}
	return m
}

func indexRuntimeProfiles(profiles []RuntimeProfile) map[RuntimeProfileID]RuntimeProfile {
	m := make(map[RuntimeProfileID]RuntimeProfile, len(profiles))
	for _, p := range profiles {
		m[p.ID] = p
	}
	return m
}

// validateCredentialDomains enforces "every credential references exactly
// one quota domain": QuotaDomainRef must be non-empty and must resolve.
func validateCredentialDomains(creds []Credential, domains map[DomainID]QuotaDomain) []Violation {
	var out []Violation
	for _, c := range creds {
		if !c.QuotaDomainRef.Valid() {
			out = append(out, Violation{"credential_one_domain", "credential", string(c.ID), "quota_domain_ref is empty"})
			continue
		}
		if _, ok := domains[c.QuotaDomainRef]; !ok {
			out = append(out, Violation{"credential_one_domain", "credential", string(c.ID), "quota_domain_ref does not resolve"})
		}
	}
	return out
}

// validateLaneReferences enforces "every lane references exactly one
// runtime profile and one quota domain".
func validateLaneReferences(lanes []Lane, domains map[DomainID]QuotaDomain, profiles map[RuntimeProfileID]RuntimeProfile) []Violation {
	var out []Violation
	for _, l := range lanes {
		if !l.RuntimeProfileRef.Valid() {
			out = append(out, Violation{"lane_one_profile", "lane", string(l.ID), "runtime_profile_ref is empty"})
		} else if _, ok := profiles[l.RuntimeProfileRef]; !ok {
			out = append(out, Violation{"lane_one_profile", "lane", string(l.ID), "runtime_profile_ref does not resolve"})
		}
		if !l.QuotaDomainRef.Valid() {
			out = append(out, Violation{"lane_one_domain", "lane", string(l.ID), "quota_domain_ref is empty"})
		} else if _, ok := domains[l.QuotaDomainRef]; !ok {
			out = append(out, Violation{"lane_one_domain", "lane", string(l.ID), "quota_domain_ref does not resolve"})
		}
	}
	return out
}

// validateBucketOwnership enforces "bucket ownership is per domain, never
// per credential".
func validateBucketOwnership(buckets []BucketRow) []Violation {
	var out []Violation
	for _, b := range buckets {
		switch b.Owner {
		case BucketOwnerQuotaDomain:
			// The only legal owner (R-21.24): no violation.
		case BucketOwnerCredential:
			out = append(out, Violation{"bucket_domain_owned", "credential", b.OwnerID, "bucket is owned by a credential, not its api_project domain"})
		default:
			// Fail-closed (06-FORGE-SPEC §5.16): an unrecognised owner
			// kind is a violation, never silently accepted as valid.
			out = append(out, Violation{"bucket_domain_owned", string(b.Owner), b.OwnerID, "unknown bucket owner kind"})
		}
	}
	return out
}

// validateQuarantinePropagation enforces "a lane whose quota domain is
// quarantined is itself quarantined".
func validateQuarantinePropagation(lanes []Lane, domains map[DomainID]QuotaDomain) []Violation {
	var out []Violation
	for _, l := range lanes {
		d, ok := domains[l.QuotaDomainRef]
		if !ok || !d.Quarantined {
			continue
		}
		if l.Health != LaneHealthQuarantined {
			out = append(out, Violation{"quarantine_propagation", "lane", string(l.ID), "quota domain is quarantined but lane health is not"})
		}
	}
	return out
}

// nilCredentialAllowedRuntimes is the R-21.107 nil-credential allowlist:
// a subscription_window domain on one of these four runtimes may have a
// nil CredentialRef.
var nilCredentialAllowedRuntimes = map[RuntimeKind]bool{
	RuntimeClaudeCLI: true, RuntimeCodex: true, RuntimeAntigravity: true, RuntimeOpenCode: true,
}

// validateCredentialJoin enforces R-21.107: a non-nil Lane.CredentialRef
// must resolve to a credential whose quota_domain_ref, account_ref
// (reached through that domain) and runtime_profile_ref equal the ones
// the lane itself reaches. A nil CredentialRef is permitted only for a
// subscription_window domain on claude-cli/codex/antigravity/opencode, and
// for the ollama runtime; every other nil is a violation.
func validateCredentialJoin(lanes []Lane, creds map[CredentialID]Credential, domains map[DomainID]QuotaDomain, profiles map[RuntimeProfileID]RuntimeProfile) []Violation {
	var out []Violation
	for _, l := range lanes {
		if l.CredentialRef == nil {
			if v, bad := nilCredentialViolation(l, domains, profiles); bad {
				out = append(out, v)
			}
			continue
		}
		cred, ok := creds[*l.CredentialRef]
		if !ok {
			out = append(out, Violation{"credential_join", "lane", string(l.ID), "credential_ref does not resolve"})
			continue
		}
		if cred.QuotaDomainRef != l.QuotaDomainRef {
			out = append(out, Violation{"credential_join", "lane", string(l.ID), "credential's quota_domain_ref differs from the lane's"})
		}
		if cred.RuntimeProfileRef != l.RuntimeProfileRef {
			out = append(out, Violation{"credential_join", "lane", string(l.ID), "credential's runtime_profile_ref differs from the lane's"})
		}
	}
	return out
}

// nilCredentialViolation reports whether lane's nil CredentialRef is
// disallowed given its resolved domain kind and runtime.
func nilCredentialViolation(lane Lane, domains map[DomainID]QuotaDomain, profiles map[RuntimeProfileID]RuntimeProfile) (Violation, bool) {
	profile, ok := profiles[lane.RuntimeProfileRef]
	if ok && profile.Runtime == RuntimeOllama {
		return Violation{}, false
	}
	domain, domainOK := domains[lane.QuotaDomainRef]
	if domainOK && domain.Kind == QuotaDomainSubscriptionWindow && ok && nilCredentialAllowedRuntimes[profile.Runtime] {
		return Violation{}, false
	}
	return Violation{"credential_join", "lane", string(lane.ID), "nil credential_ref is not allowed for this domain kind/runtime"}, true
}
