package topology

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newEvidenceStore returns a migrated store holding two discover domains,
// dom-a (account acct-a) and dom-b (account acct-b).
func newEvidenceStore(t *testing.T) *Store {
	t.Helper()
	isolatedRoot(t)
	s, _, _ := newReopenableStore(t)
	ctx := context.Background()
	for _, n := range []string{"a", "b"} {
		acct := Account{ID: AccountID("acct-" + n), Provider: "prov-" + n, Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}
		if err := s.UpsertAccount(ctx, acct); err != nil {
			t.Fatalf("account: %v", err)
		}
		dom := QuotaDomain{ID: DomainID("dom-" + n), AccountRef: acct.ID, Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}
		if err := s.UpsertQuotaDomain(ctx, dom); err != nil {
			t.Fatalf("domain: %v", err)
		}
	}
	return s
}

func testKey(n string) EvidenceKey {
	return EvidenceKey{Provider: "prov-" + n, InstanceID: "inst-" + n, Endpoint: "https://endpoint.invalid/" + n, AuthRef: "vault-ref-" + n, CredentialFingerprint: "fp-" + n}
}

func passingEvidence(id DomainID, n string) BillingEvidence {
	return BillingEvidence{DomainID: id, Key: testKey(n), PromotedBy: "operator", TestOutcome: EvidenceOutcomePassed,
		ProbeKind: "paid_model_call", ProbeStatus: 200, ProbeEndpoint: "https://endpoint.invalid/probe",
		ObservedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
}

// requireTierAndRows asserts domain id's tier and evidence row count.
func requireTierAndRows(t *testing.T, s *Store, id DomainID, tier BillingTier, rows int) []BillingEvidence {
	t.Helper()
	ctx := context.Background()
	d, err := s.GetQuotaDomain(ctx, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	ev, err := s.BillingEvidenceFor(ctx, id)
	if err != nil {
		t.Fatalf("evidence %s: %v", id, err)
	}
	if d.BillingTier != tier || len(ev) != rows {
		t.Fatalf("%s: tier=%s rows=%d, want tier=%s rows=%d", id, d.BillingTier, len(ev), tier, rows)
	}
	return ev
}

func TestPromotePaidWritesEvidenceAtomically(t *testing.T) {
	ctx := context.Background()
	s := newEvidenceStore(t)
	injected := cascade.New(cascade.KindInternal, "injected failure after evidence insert")
	afterEvidenceInsert = func() error { return injected }
	err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{passingEvidence("dom-a", "a")})
	afterEvidenceInsert = nil
	requireSentinel(t, err, injected, "")
	requireTierAndRows(t, s, "dom-a", BillingTierDiscover, 0)

	want := passingEvidence("dom-a", "a")
	if err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{want}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	ev := requireTierAndRows(t, s, "dom-a", BillingTierPaid, 1)
	if ev[0] != want {
		t.Fatalf("stored evidence %+v, want %+v", ev[0], want)
	}
	requireTierAndRows(t, s, "dom-b", BillingTierDiscover, 0)
}

func TestPromoteRefusesWithoutTestEvidence(t *testing.T) {
	ctx := context.Background()
	s := newEvidenceStore(t)
	failed := passingEvidence("dom-a", "a")
	failed.TestOutcome = "failed"
	noBy := passingEvidence("dom-a", "a")
	noBy.PromotedBy = ""
	cases := []struct {
		name string
		ev   []BillingEvidence
		want string
	}{
		{"empty", nil, "no evidence rows"},
		{"outcome", []BillingEvidence{passingEvidence("dom-a", "a"), failed}, "test outcome is not passed"},
		{"promoted_by", []BillingEvidence{noBy}, "has no PromotedBy"},
		{"domain", []BillingEvidence{passingEvidence("dom-b", "b")}, "names domain dom-b"},
	}
	for _, c := range cases {
		requireSentinel(t, s.PromoteDomain(ctx, "dom-a", c.ev), ErrBillingEvidenceRefused, c.want)
		requireTierAndRows(t, s, "dom-a", BillingTierDiscover, 0)
	}
	err := s.PromoteDomain(ctx, "ghost", []BillingEvidence{passingEvidence("ghost", "a")})
	requireSentinel(t, err, ErrTopologyNotFound, "ghost")
	if refs, err := s.listEvidenceRefs(ctx, ``); err != nil || len(refs) != 0 {
		t.Fatalf("refused promotions left rows: %d, %v", len(refs), err)
	}
	// Paired positive: the same store accepts valid evidence.
	if err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{passingEvidence("dom-a", "a")}); err != nil {
		t.Fatalf("valid promote: %v", err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierPaid, 1)
}

func TestDemoteAlwaysAllowed(t *testing.T) {
	ctx := context.Background()
	s := newEvidenceStore(t)
	if err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{passingEvidence("dom-a", "a")}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := s.DemoteDomain(ctx, "dom-a"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierDiscover, 1)
	if err := s.DemoteDomain(ctx, "dom-b"); err != nil {
		t.Fatalf("demote a discover domain: %v", err)
	}
	requireSentinel(t, s.DemoteDomain(ctx, "ghost"), ErrTopologyNotFound, "ghost")
	if err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{passingEvidence("dom-a", "a")}); err != nil {
		t.Fatalf("re-promote: %v", err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierPaid, 2)
}

func TestPruneBillingEvidence(t *testing.T) {
	ctx := context.Background()
	s := newEvidenceStore(t)
	for _, n := range []string{"a", "b"} {
		id := DomainID("dom-" + n)
		if err := s.PromoteDomain(ctx, id, []BillingEvidence{passingEvidence(id, n)}); err != nil {
			t.Fatalf("promote %s: %v", id, err)
		}
	}
	_, err := s.PruneBillingEvidence(ctx, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil key fn: %v, want KindInvalidInput", err)
	}

	denied := cascade.New(cascade.KindPermissionDenied, "key store locked")
	n, err := s.PruneBillingEvidence(ctx, func(_ context.Context, a AccountID) (EvidenceKey, error) {
		if a == "acct-b" {
			return EvidenceKey{}, denied
		}
		return EvidenceKey{}, cascade.New(cascade.KindNotFound, "gone")
	})
	requireSentinel(t, err, denied, "acct-b")
	if n != 0 || !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("denied prune: n=%d err=%v", n, err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierPaid, 1)
	requireTierAndRows(t, s, "dom-b", BillingTierPaid, 1)

	// acct-a's key changed, acct-b's still matches.
	n, err = s.PruneBillingEvidence(ctx, func(_ context.Context, a AccountID) (EvidenceKey, error) {
		if a == "acct-b" {
			return testKey("b"), nil
		}
		k := testKey("a")
		k.CredentialFingerprint = "fp-rotated"
		return k, nil
	})
	if err != nil || n != 1 {
		t.Fatalf("prune changed key: n=%d err=%v", n, err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierDiscover, 0)
	requireTierAndRows(t, s, "dom-b", BillingTierPaid, 1)
	requireForgetThenPruneNotFound(ctx, t, s)
}

// requireForgetThenPruneNotFound proves Forget touches only the named
// provider and a KindNotFound key deletes the row.
func requireForgetThenPruneNotFound(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	if err := s.PromoteDomain(ctx, "dom-a", []BillingEvidence{passingEvidence("dom-a", "a")}); err != nil {
		t.Fatalf("re-promote: %v", err)
	}
	if n, err := s.ForgetProviderEvidence(ctx, "prov-b"); err != nil || n != 1 {
		t.Fatalf("forget prov-b: n=%d err=%v", n, err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierPaid, 1)
	requireTierAndRows(t, s, "dom-b", BillingTierDiscover, 0)
	if _, err := s.ForgetProviderEvidence(ctx, " "); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty provider: %v, want KindInvalidInput", err)
	}
	n, err := s.PruneBillingEvidence(ctx, func(context.Context, AccountID) (EvidenceKey, error) {
		return EvidenceKey{}, cascade.New(cascade.KindNotFound, "gone")
	})
	if err != nil || n != 1 {
		t.Fatalf("prune not-found: n=%d err=%v", n, err)
	}
	requireTierAndRows(t, s, "dom-a", BillingTierDiscover, 0)
}
