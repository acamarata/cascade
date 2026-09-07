package intake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
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

func TestPoolJoinIndexUsesMemoryRegistry(t *testing.T) {
	reg := NewMemoryRegistry()
	ctx := context.Background()
	if got := poolJoinIndex(ctx, reg, "gf"); got != 0 {
		t.Fatalf("expected the first join to get index 0, got %d", got)
	}
	if got := poolJoinIndex(ctx, reg, "gf"); got != 1 {
		t.Fatalf("expected the second join to advance to index 1, got %d", got)
	}
}

func TestPoolJoinIndexNonMemoryRegistryReturnsZero(t *testing.T) {
	if got := poolJoinIndex(context.Background(), stubRegistry{}, "gf"); got != 0 {
		t.Fatalf("expected a non-MemoryRegistry to return 0, got %d", got)
	}
}

// stubRegistry is a minimal Registry the future S-20.T2 domain will
// replace; it exists only to prove poolJoinIndex degrades gracefully for
// any Registry implementation that is not this ticket's MemoryRegistry.
type stubRegistry struct{}

func (stubRegistry) UpsertProvider(context.Context, ProviderRecord) error { return nil }
func (stubRegistry) GetProvider(context.Context, string) (ProviderRecord, error) {
	return ProviderRecord{}, cascade.New(cascade.KindNotFound, "stub")
}
func (stubRegistry) ListPool(context.Context, string) ([]ProviderRecord, error) { return nil, nil }

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
