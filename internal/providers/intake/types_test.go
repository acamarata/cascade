package intake

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for the durable stub
)

func TestDriverKindValid(t *testing.T) {
	for _, k := range []DriverKind{DriverAnthropic, DriverOpenAICompat, DriverGemini, DriverOllama, DriverLocalLLM} {
		if !k.Valid() {
			t.Errorf("DriverKind %q should be valid", k)
		}
	}
	if DriverKind("made-up").Valid() {
		t.Error("an unrecognised driver kind must not be valid")
	}
}

func TestAuthTypeValid(t *testing.T) {
	if !AuthKey.Valid() || !AuthOAuth.Valid() {
		t.Fatal("AuthKey and AuthOAuth must be valid")
	}
	if AuthType("bearer").Valid() {
		t.Error("an unrecognised auth type must not be valid")
	}
}

func TestKeyRefForNeverEqualsRawName(t *testing.T) {
	ref := keyRefFor("myclaude", "key")
	if ref.String() == "myclaude" {
		t.Fatal("a vault-key ref must never equal the bare provider name")
	}
	if !strings.HasPrefix(ref.String(), "provider.myclaude.") {
		t.Fatalf("unexpected ref shape: %q", ref)
	}
}

func TestMemoryRegistryUpsertIsIdempotent(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	rec := ProviderRecord{Name: "p1", Driver: DriverAnthropic}
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	rec.BaseURL = "https://example.test"
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BaseURL != "https://example.test" {
		t.Fatalf("second upsert did not update the existing record, got %+v", got)
	}
}

func TestMemoryRegistryUpsertRejectsEmptyName(t *testing.T) {
	if err := NewMemoryRegistry().UpsertProvider(context.Background(), ProviderRecord{}); err == nil {
		t.Fatal("expected an error for an empty provider name")
	}
}

func TestMemoryRegistryGetProviderNotFound(t *testing.T) {
	if _, err := NewMemoryRegistry().GetProvider(context.Background(), "nope"); err == nil {
		t.Fatal("expected a not-found error for an unknown provider")
	}
}

func TestMemoryRegistryListPoolSortedByName(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := reg.UpsertProvider(ctx, ProviderRecord{Name: name, Pool: "gf"}); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	if err := reg.UpsertProvider(ctx, ProviderRecord{Name: "other", Pool: "different"}); err != nil {
		t.Fatal(err)
	}
	members, err := reg.ListPool(ctx, "gf")
	if err != nil {
		t.Fatalf("list pool: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 pool members, got %d", len(members))
	}
	for i := 1; i < len(members); i++ {
		if members[i-1].Name > members[i].Name {
			t.Fatalf("pool members are not sorted: %+v", members)
		}
	}
}

func TestMemoryRegistryNextPoolIndexAdvances(t *testing.T) {
	reg := NewMemoryRegistry()
	first := reg.nextPoolIndex("gf")
	second := reg.nextPoolIndex("gf")
	if second != first+1 {
		t.Fatalf("expected the pool index to advance monotonically, got %d then %d", first, second)
	}
}

// TestPoolJoinIndexDistinctOnDurableRegistry adds three members of pool p
// through Add over a durable, non-MemoryRegistry store: indices {0,1,2},
// read back from storage, an existing member keeps its own index, and a
// join after a removal takes max+1 rather than the member count.
func TestPoolJoinIndexDistinctOnDurableRegistry(t *testing.T) {
	ctx := context.Background()
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	reg := newStubRegistry(t)
	deps.Registry = reg
	for _, name := range []string{"c", "a", "b"} {
		req := AddRequest{Name: name, Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: "p"}
		if _, err := Add(ctx, deps, req); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	members, err := reg.ListPool(ctx, "p")
	got := map[string]int{}
	for _, m := range members {
		got[m.Name] = m.PoolIndex
	}
	if err != nil || len(members) != 3 || got["c"] != 0 || got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("stored pool = %+v (err %v), want c:0 a:1 b:2", members, err)
	}
	if idx, err := poolJoinIndex(ctx, reg, "p", "a"); err != nil || idx != 1 {
		t.Fatalf("existing member a: index %d (err %v), want its own index 1", idx, err)
	}
	keep, err := Add(ctx, deps, AddRequest{Name: "a", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value")})
	if err != nil || keep.Record.Pool != "p" || keep.Record.PoolIndex != 1 {
		t.Fatalf("re-add without pool: %+v (err %v), want pool p index 1", keep.Record, err)
	}
	if _, err := Add(ctx, deps, AddRequest{Name: "a", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: "q"}); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("re-add into pool q: %v, want KindConflict", err)
	}
	// Gap: removing c (index 0) leaves {a:1,b:2}; d takes max+1 = 3, not the
	// count 2 that collides with b. The first member of an empty pool gets 0.
	if err := reg.reg.DeleteProvider(ctx, "c"); err != nil {
		t.Fatalf("delete c: %v", err)
	}
	for pool, want := range map[string]int{"p": 3, "empty": 0} {
		rec, err := Add(ctx, deps, AddRequest{Name: "d-" + pool, Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: pool})
		if err != nil || rec.Record.PoolIndex != want {
			t.Fatalf("new member of pool %s: %+v (err %v), want index %d", pool, rec.Record, err, want)
		}
	}
}

// TestPoolJoinIndexReadFailureRefusesAdd is the fail-closed half: a
// GetProvider or ListPool failure refuses Add with that same error, before
// any credential is stored and with nothing written to the registry.
func TestPoolJoinIndexReadFailureRefusesAdd(t *testing.T) {
	for _, field := range []string{"get", "list"} {
		ctx := context.Background()
		deps, custody := testDeps(t, anthropicSuccessDoer(t))
		reg := newStubRegistry(t)
		boom := cascade.New(cascade.KindUnavailable, "stub: "+field+" failed")
		if field == "get" {
			reg.getErr = boom
		} else {
			reg.listErr = boom
		}
		deps.Registry = reg
		req := AddRequest{Name: "a", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value"), Pool: "p"}
		_, err := Add(ctx, deps, req)
		lanes, lerr := reg.reg.ListLanes(ctx)
		if err != boom || err.Error() != boom.Error() || lerr != nil || len(lanes) != 0 || len(custody.setCall) != 0 {
			t.Fatalf("%s failure: err %v, lanes %+v (%v), vault writes %v; want the stub error and no writes",
				field, err, lanes, lerr, custody.setCall)
		}
	}
}

// stubRegistry is a Registry that is not a MemoryRegistry: it keeps pool
// membership in the durable providers registry over t.TempDir, on lanes
// named "<pool>/<name>" as the CLI's adapter does. getErr/listErr, when
// set, make GetProvider/ListPool fail.
type stubRegistry struct {
	reg             *registry.Registry
	getErr, listErr error
}

// newStubRegistry opens and migrates a providers.db under t.TempDir().
func newStubRegistry(t *testing.T) *stubRegistry {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clk := fixedClock{now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatal(err)
	}
	return &stubRegistry{reg: registry.NewRegistry(db, clk)}
}

func (s *stubRegistry) UpsertProvider(ctx context.Context, rec ProviderRecord) error {
	if err := s.reg.UpsertProvider(ctx, registry.ProviderRecord{
		Name: rec.Name, Driver: registry.DriverKind(rec.Driver), Auth: registry.AuthType(rec.Auth),
		AuthRef: registry.VaultKeyRef(rec.AuthRef), AccountKind: registry.AccountPersonal,
		Tier: registry.TierMid, HealthStatus: registry.HealthUnknown,
	}); err != nil {
		return err
	}
	return s.reg.UpsertLane(ctx, registry.LaneRecord{
		LaneName: rec.Pool + "/" + rec.Name, ProviderName: rec.Name, Weight: 1, PoolMembership: rec.Pool,
		PoolIndex: rec.PoolIndex, Capacity: registry.CapacityAPICredit, State: registry.LaneStateUnknown,
	})
}

func (s *stubRegistry) GetProvider(ctx context.Context, name string) (ProviderRecord, error) {
	if s.getErr != nil {
		return ProviderRecord{}, s.getErr
	}
	lanes, err := s.reg.ListLanes(ctx)
	if err != nil {
		return ProviderRecord{}, err
	}
	for _, l := range lanes {
		if l.ProviderName == name {
			return ProviderRecord{Name: name, Pool: l.PoolMembership, PoolIndex: l.PoolIndex}, nil
		}
	}
	return ProviderRecord{}, cascade.Newf(cascade.KindNotFound, "stub: no provider %q", name)
}

func (s *stubRegistry) ListPool(ctx context.Context, pool string) ([]ProviderRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	lanes, err := s.reg.ListPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderRecord, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, ProviderRecord{Name: l.ProviderName, Pool: l.PoolMembership, PoolIndex: l.PoolIndex})
	}
	return out, nil
}

func TestOAuthClockAdapterDelegatesToClock(t *testing.T) {
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := oauthClockAdapter{c: fixedClock{now: want}}.Now()
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestDepsGetenvDefaultsToOSEnviron(t *testing.T) {
	d := Deps{}
	if d.getenv() == nil {
		t.Fatal("expected a non-nil default getenv")
	}
}

func TestDepsValidateRejectsMissingDependency(t *testing.T) {
	if err := (Deps{}).validate(); err == nil {
		t.Fatal("expected an error for an empty Deps")
	}
}

// errRegistry always refuses UpsertProvider, to exercise Add's own error
// path for a registry write failure (a future S-20.T2 registry's own
// storage-layer refusals surface the same way).
type errRegistry struct{ *MemoryRegistry }

func (errRegistry) UpsertProvider(context.Context, ProviderRecord) error {
	return cascade.New(cascade.KindUnavailable, "errRegistry: refuses every write")
}

func TestAddPropagatesRegistryUpsertError(t *testing.T) {
	deps, _ := testDeps(t, anthropicSuccessDoer(t))
	deps.Registry = errRegistry{MemoryRegistry: NewMemoryRegistry()}
	req := AddRequest{Name: "myclaude", Credential: CredentialKey, KeyValue: []byte("sk-ant-test-value")}
	if _, err := Add(context.Background(), deps, req); err == nil {
		t.Fatal("expected Add to propagate a registry upsert error")
	}
}
