package topology

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// seedLookupDomain writes one account, domain, profile, credential and lane
// through the store API and returns the lane id.
func seedLookupDomain(t *testing.T, s *Store, domain DomainID, cred CredentialID, health CredentialHealth) LaneID {
	t.Helper()
	ctx := context.Background()
	acct := Account{ID: "acct-" + AccountID(domain), Provider: "openai_compat", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}
	profile := RuntimeProfile{ID: RuntimeProfileID("prof-" + string(domain)), Runtime: RuntimeAPI}
	steps := []func() error{
		func() error { return s.UpsertAccount(ctx, acct) },
		func() error {
			return s.UpsertQuotaDomain(ctx, QuotaDomain{ID: domain, AccountRef: acct.ID, Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover})
		},
		func() error { return s.UpsertRuntimeProfile(ctx, profile) },
		func() error {
			return s.UpsertCredential(ctx, Credential{ID: cred, AccountRef: acct.ID, QuotaDomainRef: domain,
				SecretRef: VaultKeyRef("provider." + string(cred) + ".key"), RuntimeProfileRef: profile.ID, Health: health})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed %s: %v", domain, err)
		}
	}
	id := LaneIDFor(profile.ID, domain, "", "", EffortNA, InteractionInteractive)
	lane := Lane{ID: id, RuntimeProfileRef: profile.ID, QuotaDomainRef: domain, CredentialRef: &cred, Effort: EffortNA,
		InteractionClass: InteractionInteractive, Health: LaneHealthAvailable, LaneClass: LaneClassUnranked,
		BaseShadowPrice: 1, DiscoveredAt: newTestClock().Now()}
	if err := s.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	return id
}

func TestDomainForCredential(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newReopenableStore(t)
	seedLookupDomain(t, s, "live:api", "cred-live", CredentialOK)
	seedLookupDomain(t, s, "quar:api", "cred-quar", CredentialQuarantined)
	seedLookupDomain(t, s, "qdom:api", "cred-qdom", CredentialOK)
	qd, err := s.GetQuotaDomain(ctx, "qdom:api")
	if err != nil {
		t.Fatalf("GetQuotaDomain: %v", err)
	}
	qd.Quarantined = true
	if err := s.UpsertQuotaDomain(ctx, qd); err != nil {
		t.Fatalf("quarantine the domain: %v", err)
	}
	retired := seedLookupDomain(t, s, "gone:api", "cred-gone", CredentialOK)
	if err := s.RetireLane(ctx, retired, newTestClock().Now()); err != nil {
		t.Fatalf("RetireLane: %v", err)
	}

	got, err := s.DomainForCredential(ctx, "cred-live")
	if err != nil || got != "live:api" {
		t.Fatalf("DomainForCredential(cred-live) = %q, %v; want live:api", got, err)
	}
	for _, c := range []struct {
		cred CredentialID
		kind cascade.Kind
	}{
		{"cred-absent", cascade.KindNotFound},
		{"cred-quar", cascade.KindUnavailable},
		{"cred-qdom", cascade.KindUnavailable},
		{"cred-gone", cascade.KindUnavailable},
	} {
		got, err := s.DomainForCredential(ctx, c.cred)
		if kind, _ := cascade.KindOf(err); err == nil || kind != c.kind || got != "" {
			t.Errorf("DomainForCredential(%s) = %q, %v; want kind %s and no domain", c.cred, got, err, c.kind)
			continue
		}
		if wrapped := wrapsNoLive(err); wrapped != (c.kind == cascade.KindUnavailable) {
			t.Errorf("DomainForCredential(%s) = %v; ErrNoLiveQuotaDomain wrapped %v, want %v", c.cred, err, wrapped, !wrapped)
		}
	}
}

// wrapsNoLive reports ErrNoLiveQuotaDomain in err's chain by identity (errors.Is is Kind-only).
func wrapsNoLive(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == ErrNoLiveQuotaDomain {
			return true
		}
	}
	return false
}
