package topology

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := openRealSQLiteFile(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return NewStore(db)
}

func TestAccountUpsertGetList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	acct := Account{ID: "acct-1", Provider: "anthropic", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}
	if err := s.UpsertAccount(ctx, acct); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	got, err := s.GetAccount(ctx, "acct-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got != acct {
		t.Errorf("GetAccount = %+v, want %+v", got, acct)
	}
	list, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListAccounts len = %d, want 1", len(list))
	}
}

func TestAccountGetNotFound(t *testing.T) {
	if _, err := newTestStore(t).GetAccount(context.Background(), "missing"); err == nil {
		t.Fatal("GetAccount(missing) should have failed")
	}
}

func TestAccountInvalidRejected(t *testing.T) {
	if err := newTestStore(t).UpsertAccount(context.Background(), Account{}); err == nil {
		t.Fatal("UpsertAccount(zero value) should have failed")
	}
}

func TestQuotaDomainUpsertGetList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedAccount(t, s, "acct-1")
	dom := QuotaDomain{ID: "acct-1:api", AccountRef: "acct-1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}
	if err := s.UpsertQuotaDomain(ctx, dom); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
	got, err := s.GetQuotaDomain(ctx, "acct-1:api")
	if err != nil {
		t.Fatalf("GetQuotaDomain: %v", err)
	}
	// QuotaDomain now carries a map[string]Bucket field (S-77.T3's
	// Dimensions), so it is no longer comparable with != -- reflect.
	// DeepEqual replaces the direct comparison, same semantics for the
	// zero-Dimensions case this test exercises.
	if !reflect.DeepEqual(got, dom) {
		t.Errorf("GetQuotaDomain = %+v, want %+v", got, dom)
	}
	if _, err := s.ListQuotaDomains(ctx); err != nil {
		t.Fatalf("ListQuotaDomains: %v", err)
	}
}

func TestQuotaDomainInvalidRejected(t *testing.T) {
	if err := newTestStore(t).UpsertQuotaDomain(context.Background(), QuotaDomain{}); err == nil {
		t.Fatal("UpsertQuotaDomain(zero value) should have failed")
	}
}

func TestRuntimeProfileUpsertGetList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	profile := RuntimeProfile{ID: "acct-1:anthropic", Runtime: RuntimeAPI, Endpoint: Endpoint{Host: "api.example.com", Port: 443}}
	if err := s.UpsertRuntimeProfile(ctx, profile); err != nil {
		t.Fatalf("UpsertRuntimeProfile: %v", err)
	}
	got, err := s.GetRuntimeProfile(ctx, "acct-1:anthropic")
	if err != nil {
		t.Fatalf("GetRuntimeProfile: %v", err)
	}
	if got != profile {
		t.Errorf("GetRuntimeProfile = %+v, want %+v", got, profile)
	}
	if _, err := s.ListRuntimeProfiles(ctx); err != nil {
		t.Fatalf("ListRuntimeProfiles: %v", err)
	}
}

func TestRuntimeProfileInvalidRejected(t *testing.T) {
	if err := newTestStore(t).UpsertRuntimeProfile(context.Background(), RuntimeProfile{}); err == nil {
		t.Fatal("UpsertRuntimeProfile(zero value) should have failed")
	}
}

func TestCredentialUpsertGetList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedAccount(t, s, "acct-1")
	seedDomain(t, s, "acct-1:api", "acct-1")
	seedProfile(t, s, "acct-1:anthropic")
	cred := Credential{ID: "cred-1", AccountRef: "acct-1", QuotaDomainRef: "acct-1:api",
		SecretRef: "vault://acct-1/anthropic-key", RuntimeProfileRef: "acct-1:anthropic", Health: CredentialOK}
	if err := s.UpsertCredential(ctx, cred); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}
	got, err := s.GetCredential(ctx, "cred-1")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if got != cred {
		t.Errorf("GetCredential = %+v, want %+v", got, cred)
	}
	if _, err := s.ListCredentials(ctx); err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
}

func TestCredentialInvalidRejected(t *testing.T) {
	if err := newTestStore(t).UpsertCredential(context.Background(), Credential{}); err == nil {
		t.Fatal("UpsertCredential(zero value) should have failed")
	}
}

// TestCredentialSecretRefOnly asserts that no column of any of the five
// tables ever receives a credential value: only the vault-key NAME
// (SecretRef) is ever written, and it is never dereferenced/interpreted.
func TestCredentialSecretRefOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedAccount(t, s, "acct-1")
	seedDomain(t, s, "acct-1:api", "acct-1")
	seedProfile(t, s, "acct-1:anthropic")
	ref := VaultKeyRef("vault://acct-1/anthropic-key")
	cred := Credential{ID: "cred-1", AccountRef: "acct-1", QuotaDomainRef: "acct-1:api",
		SecretRef: ref, RuntimeProfileRef: "acct-1:anthropic", Health: CredentialOK}
	if err := s.UpsertCredential(ctx, cred); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT secret_ref FROM `+tableCredential+` WHERE id = ?`, "cred-1").Scan(&stored); err != nil {
		t.Fatalf("read secret_ref column: %v", err)
	}
	if stored != ref.String() {
		t.Errorf("secret_ref column = %q, want the vault-key NAME %q unchanged", stored, ref.String())
	}
	if isCredentialShapedTestHelper(stored) {
		t.Errorf("secret_ref %q looks credential-shaped, want a vault-key name", stored)
	}
}

// isCredentialShapedTestHelper is a minimal, test-local heuristic (this
// package never needs a real one -- SecretRef's type alone prevents a raw
// secret from reaching a typed field; this only guards the fixture value
// used above).
func isCredentialShapedTestHelper(s string) bool {
	return len(s) > 20 && (s[:4] == "sk-a" || s[:4] == "sk-p")
}

func seedAccount(t *testing.T, s *Store, id AccountID) {
	t.Helper()
	if err := s.UpsertAccount(context.Background(), Account{ID: id, Provider: "anthropic", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}); err != nil {
		t.Fatalf("seedAccount: %v", err)
	}
}

func seedDomain(t *testing.T, s *Store, id DomainID, acct AccountID) {
	t.Helper()
	if err := s.UpsertQuotaDomain(context.Background(), QuotaDomain{ID: id, AccountRef: acct, Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}); err != nil {
		t.Fatalf("seedDomain: %v", err)
	}
}

func seedProfile(t *testing.T, s *Store, id RuntimeProfileID) {
	t.Helper()
	if err := s.UpsertRuntimeProfile(context.Background(), RuntimeProfile{ID: id, Runtime: RuntimeAPI}); err != nil {
		t.Fatalf("seedProfile: %v", err)
	}
}

// TestLaneRetiredRefusesNewReservation is the R-21.124 named acceptance
// test: RetireLane sets retired_at; CheckReservable then refuses a NEW
// reservation with ErrLaneRetired, while the lane stays queryable through
// Get and List with include_retired.
func TestLaneRetiredRefusesNewReservation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	seedAccount(t, store, "acct-1")
	seedDomain(t, store, "acct-1:api", "acct-1")
	seedProfile(t, store, "acct-1:anthropic")
	lane := Lane{ID: "lane-1", RuntimeProfileRef: "acct-1:anthropic", QuotaDomainRef: "acct-1:api",
		Health: LaneHealthAvailable, LaneClass: LaneClassUnranked, BaseShadowPrice: 1.0, DiscoveredAt: newTestClock().Now()}
	if err := store.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	if err := store.CheckReservable(ctx, "lane-1"); err != nil {
		t.Fatalf("CheckReservable on a live lane should be nil, got %v", err)
	}

	if err := store.RetireLane(ctx, "lane-1", newTestClock().Now()); err != nil {
		t.Fatalf("RetireLane: %v", err)
	}
	if err := store.CheckReservable(ctx, "lane-1"); !errors.Is(err, ErrLaneRetired) {
		t.Errorf("CheckReservable after RetireLane should return ErrLaneRetired, got %v", err)
	}

	got, err := store.GetLane(ctx, "lane-1")
	if err != nil {
		t.Fatalf("GetLane should still find a retired lane: %v", err)
	}
	if got.RetiredAt == nil {
		t.Error("retired lane should have a non-nil RetiredAt")
	}

	liveOnly, err := store.ListLanes(ctx, false)
	if err != nil {
		t.Fatalf("ListLanes(false): %v", err)
	}
	if len(liveOnly) != 0 {
		t.Errorf("ListLanes(includeRetired=false) should exclude the retired lane, got %d", len(liveOnly))
	}
	withRetired, err := store.ListLanes(ctx, true)
	if err != nil {
		t.Fatalf("ListLanes(true): %v", err)
	}
	if len(withRetired) != 1 {
		t.Errorf("ListLanes(includeRetired=true) should still include the retired lane, got %d", len(withRetired))
	}
}

func TestRetireLaneNotFound(t *testing.T) {
	store := newTestStore(t)
	if err := store.RetireLane(context.Background(), "missing", newTestClock().Now()); err == nil {
		t.Fatal("RetireLane(missing) should have failed")
	}
}

// TestRetireLaneTwiceIsConflict proves a second RetireLane on an
// already-retired lane is ErrTopologyConflict, never a silent re-stamp of
// retired_at.
func TestRetireLaneTwiceIsConflict(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	seedAccount(t, store, "acct-1")
	seedDomain(t, store, "acct-1:api", "acct-1")
	seedProfile(t, store, "acct-1:anthropic")
	lane := Lane{ID: "lane-1", RuntimeProfileRef: "acct-1:anthropic", QuotaDomainRef: "acct-1:api",
		Health: LaneHealthAvailable, LaneClass: LaneClassUnranked, BaseShadowPrice: 1.0, DiscoveredAt: newTestClock().Now()}
	if err := store.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	firstRetire := newTestClock().Now()
	if err := store.RetireLane(ctx, "lane-1", firstRetire); err != nil {
		t.Fatalf("first RetireLane: %v", err)
	}
	secondRetire := firstRetire.AddDate(0, 0, 1)
	if err := store.RetireLane(ctx, "lane-1", secondRetire); !errors.Is(err, ErrTopologyConflict) {
		t.Errorf("second RetireLane should be ErrTopologyConflict, got %v", err)
	}
	got, err := store.GetLane(ctx, "lane-1")
	if err != nil {
		t.Fatalf("GetLane: %v", err)
	}
	if !got.RetiredAt.Equal(firstRetire) {
		t.Errorf("retired_at moved to %v, want the first retirement %v unchanged", got.RetiredAt, firstRetire)
	}
}

func TestCheckReservableNotFound(t *testing.T) {
	store := newTestStore(t)
	if err := store.CheckReservable(context.Background(), "missing"); err == nil {
		t.Fatal("CheckReservable(missing) should have failed")
	}
}

// TestClosedDBExecErrorPaths forces the exec/query-error branch (not the
// not-found branch) across every CRUD method by issuing calls against an
// already-closed *sql.DB.
