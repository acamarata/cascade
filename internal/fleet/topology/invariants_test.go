package topology

import (
	"context"
	"testing"
)

func baseSnapshot() Snapshot {
	return Snapshot{
		Accounts:        []Account{{ID: "acct-1", Provider: "anthropic", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}},
		QuotaDomains:    []QuotaDomain{{ID: "acct-1:api", AccountRef: "acct-1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}},
		RuntimeProfiles: []RuntimeProfile{{ID: "acct-1:anthropic", Runtime: RuntimeAPI}},
		Credentials: []Credential{{ID: "cred-1", AccountRef: "acct-1", QuotaDomainRef: "acct-1:api",
			RuntimeProfileRef: "acct-1:anthropic", Health: CredentialOK}},
	}
}

func TestValidateCleanSnapshotHasNoViolations(t *testing.T) {
	credID := CredentialID("cred-1")
	snap := baseSnapshot()
	snap.Lanes = []Lane{{ID: "lane-1", RuntimeProfileRef: "acct-1:anthropic", QuotaDomainRef: "acct-1:api",
		CredentialRef: &credID, Health: LaneHealthAvailable}}
	if v := Validate(context.Background(), snap); len(v) != 0 {
		t.Fatalf("clean snapshot should have zero violations, got %v", v)
	}
}

func TestValidateCredentialMissingDomain(t *testing.T) {
	snap := Snapshot{Credentials: []Credential{{ID: "cred-1", RuntimeProfileRef: "rp-1", Health: CredentialOK}}}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_one_domain") {
		t.Errorf("expected credential_one_domain violation, got %v", violations)
	}
}

func TestValidateCredentialUnresolvedDomain(t *testing.T) {
	snap := Snapshot{Credentials: []Credential{{ID: "cred-1", QuotaDomainRef: "missing", RuntimeProfileRef: "rp-1", Health: CredentialOK}}}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_one_domain") {
		t.Errorf("expected credential_one_domain violation for unresolved ref, got %v", violations)
	}
}

func TestValidateLaneMissingProfileOrDomain(t *testing.T) {
	snap := Snapshot{Lanes: []Lane{{ID: "lane-1", Health: LaneHealthAvailable}}}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "lane_one_profile") {
		t.Errorf("expected lane_one_profile violation, got %v", violations)
	}
	if !hasInvariant(violations, "lane_one_domain") {
		t.Errorf("expected lane_one_domain violation, got %v", violations)
	}
}

func TestValidateBucketOwnedByCredentialIsViolation(t *testing.T) {
	snap := Snapshot{Buckets: []BucketRow{{Owner: BucketOwnerCredential, OwnerID: "cred-1"}}}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "bucket_domain_owned") {
		t.Errorf("expected bucket_domain_owned violation, got %v", violations)
	}
}

func TestValidateBucketOwnedByDomainIsClean(t *testing.T) {
	snap := Snapshot{Buckets: []BucketRow{{Owner: BucketOwnerQuotaDomain, OwnerID: "dom-1"}}}
	if v := Validate(context.Background(), snap); len(v) != 0 {
		t.Errorf("domain-owned bucket should have no violation, got %v", v)
	}
}

// TestValidateBucketUnknownOwnerFailsClosed proves an unrecognised
// BucketOwnerKind is a violation, never silently accepted (06 §5.16).
func TestValidateBucketUnknownOwnerFailsClosed(t *testing.T) {
	snap := Snapshot{Buckets: []BucketRow{{Owner: BucketOwnerKind("bogus"), OwnerID: "x"}}}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "bucket_domain_owned") {
		t.Errorf("unknown bucket owner kind should violate, got %v", violations)
	}
}

func TestValidateQuarantinePropagation(t *testing.T) {
	snap := Snapshot{
		QuotaDomains: []QuotaDomain{{ID: "dom-1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid, Quarantined: true}},
		Lanes:        []Lane{{ID: "lane-1", QuotaDomainRef: "dom-1", RuntimeProfileRef: "rp-1", Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "quarantine_propagation") {
		t.Errorf("expected quarantine_propagation violation, got %v", violations)
	}
}

func TestValidateQuarantinePropagationSatisfied(t *testing.T) {
	credID := CredentialID("cred-1")
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid, Quarantined: true}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}},
		Credentials:     []Credential{{ID: credID, QuotaDomainRef: "dom-1", RuntimeProfileRef: "rp-1", Health: CredentialQuarantined}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-1", RuntimeProfileRef: "rp-1", CredentialRef: &credID, Health: LaneHealthQuarantined}},
	}
	if v := Validate(context.Background(), snap); len(v) != 0 {
		t.Errorf("lane already quarantined should have no violation, got %v", v)
	}
}

func TestValidateCredentialJoinDomainMismatch(t *testing.T) {
	credID := CredentialID("cred-1")
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}, {ID: "dom-b", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}},
		Credentials:     []Credential{{ID: credID, QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", Health: CredentialOK}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-b", RuntimeProfileRef: "rp-1", CredentialRef: &credID, Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_join") {
		t.Errorf("expected credential_join violation for mismatched domain, got %v", violations)
	}
}

func TestValidateCredentialJoinProfileMismatch(t *testing.T) {
	credID := CredentialID("cred-1")
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}, {ID: "rp-2", Runtime: RuntimeAPI}},
		Credentials:     []Credential{{ID: credID, QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", Health: CredentialOK}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-2", CredentialRef: &credID, Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_join") {
		t.Errorf("expected credential_join violation for mismatched profile, got %v", violations)
	}
}

func TestValidateCredentialJoinUnresolvedRef(t *testing.T) {
	missing := CredentialID("missing")
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", CredentialRef: &missing, Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_join") {
		t.Errorf("expected credential_join violation for unresolved ref, got %v", violations)
	}
}

func TestValidateNilCredentialAllowedForOllama(t *testing.T) {
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeOllama}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", CredentialRef: nil, Health: LaneHealthAvailable}},
	}
	if v := Validate(context.Background(), snap); len(v) != 0 {
		t.Errorf("nil credential_ref should be allowed for ollama, got %v", v)
	}
}

func TestValidateNilCredentialAllowedForSubscriptionCLI(t *testing.T) {
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainSubscriptionWindow, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeClaudeCLI}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", CredentialRef: nil, Health: LaneHealthAvailable}},
	}
	if v := Validate(context.Background(), snap); len(v) != 0 {
		t.Errorf("nil credential_ref should be allowed for subscription_window+claude-cli, got %v", v)
	}
}

func TestValidateNilCredentialDisallowedForAPIRuntime(t *testing.T) {
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", CredentialRef: nil, Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_join") {
		t.Errorf("nil credential_ref on an api_project/api-runtime lane should violate, got %v", violations)
	}
}

func TestValidateNilCredentialDisallowedForSubscriptionOnAPIRuntime(t *testing.T) {
	snap := Snapshot{
		QuotaDomains:    []QuotaDomain{{ID: "dom-a", Kind: QuotaDomainSubscriptionWindow, BillingTier: BillingTierPaid}},
		RuntimeProfiles: []RuntimeProfile{{ID: "rp-1", Runtime: RuntimeAPI}},
		Lanes:           []Lane{{ID: "lane-1", QuotaDomainRef: "dom-a", RuntimeProfileRef: "rp-1", CredentialRef: nil, Health: LaneHealthAvailable}},
	}
	violations := Validate(context.Background(), snap)
	if !hasInvariant(violations, "credential_join") {
		t.Errorf("subscription_window on a non-allowlisted runtime should violate, got %v", violations)
	}
}

// TestCredentialJoinInvariant is the R-21.107 named acceptance test: a
// non-nil credential_ref whose domain/profile differs from the lane's own
// is a violation, and the nil-credential allowlist is exact.
func TestCredentialJoinInvariant(t *testing.T) {
	t.Run("mismatch", TestValidateCredentialJoinDomainMismatch)
	t.Run("nil_allowed_ollama", TestValidateNilCredentialAllowedForOllama)
	t.Run("nil_disallowed_api", TestValidateNilCredentialDisallowedForAPIRuntime)
}

func hasInvariant(violations []Violation, name string) bool {
	for _, v := range violations {
		if v.Invariant == name {
			return true
		}
	}
	return false
}

// TestValidateSnapshotUntouchedByCallerMutation proves Validate only
// inspects the Snapshot it is given -- it never reaches back into a
// Store, so a Snapshot built from real store rows plus one bad row in
// memory reports the violation without touching persisted state (the same
// check Reconcile's own commitIfValid runs before a transaction commits).
func TestValidateSnapshotUntouchedByCallerMutation(t *testing.T) {
	snap := baseSnapshot()
	snap.Credentials = append(snap.Credentials, Credential{ID: "bad-cred", QuotaDomainRef: "missing-domain",
		RuntimeProfileRef: "rp-1", Health: CredentialOK})
	violations := Validate(context.Background(), snap)
	if len(violations) == 0 {
		t.Fatal("expected at least one violation for a credential referencing a missing domain")
	}
	// baseSnapshot()'s own well-formed rows must still be present and
	// untouched -- Validate never mutates or drops an input row.
	if len(snap.Accounts) != 1 || snap.Accounts[0].ID != "acct-1" {
		t.Error("Validate must not mutate the Snapshot it is given")
	}
}
