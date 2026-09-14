package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the record-level half of the lifecycle guards —
//
//	a corrupt stored record, the write path's own validation, grant-change
//	idempotency and the registry allowed-fail leg. Split from
//	lifecycle_refusals_test.go to stay under the 300-line file cap, which
//	counts test files too.
//
// SPORT: internal/plugins lifecycle-record tests (ADD).
// TestLoadMetadataReportsACorruptRecord covers the decode branch. A record
// that is present but unparseable must be reported as corrupt, never as
// absent: "not installed" would send a caller down the install path and
// overwrite whatever is really there.
func TestLoadMetadataReportsACorruptRecord(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := store.Put(ctx, HostNamespace, metadataKey("demo"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}

	_, ok, err := LoadMetadata(ctx, store, "demo")
	if err == nil {
		t.Fatal("a corrupt record decoded without error")
	}
	if ok {
		t.Error("a corrupt record reported ok=true")
	}
	if kind, typed := cascade.KindOf(err); !typed || kind != cascade.KindIntegrity {
		t.Errorf("error kind = %v (typed=%v), want %v", kind, typed, cascade.KindIntegrity)
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error = %v, want it to say the record is corrupt", err)
	}
}

// TestSaveMetadataRefusesAnEmptyName proves the write path validates too,
// so a record can never be persisted under a name no read path would
// accept back.
func TestSaveMetadataRefusesAnEmptyName(t *testing.T) {
	store := storetest.NewMemStore()
	for _, name := range badNames {
		if err := SaveMetadata(context.Background(), store, PluginMetadata{Name: name}); err == nil {
			t.Errorf("SaveMetadata accepted the malformed name %q", name)
		}
	}
}

// TestChangePermsIsIdempotentInBothDirections covers applyGrantChange's two
// no-op arms: granting a capability already held, and revoking one that is
// already absent. Both must converge rather than duplicating an entry or
// erroring, since a repair script re-applies the desired state blindly.
func TestChangePermsIsIdempotentInBothDirections(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{
		Name: "demo", Enabled: true, Grants: []string{"net.http"},
	}); err != nil {
		t.Fatal(err)
	}

	rec, err := ChangePerms(ctx, store, "demo", "net.http", true, true, false)
	if err != nil {
		t.Fatalf("re-granting an existing capability: %v", err)
	}
	if len(rec.Grants) != 1 {
		t.Fatalf("grants = %v, want the capability present exactly once", rec.Grants)
	}

	rec, err = ChangePerms(ctx, store, "demo", "never-granted", false, true, false)
	if err != nil {
		t.Fatalf("revoking an absent capability: %v", err)
	}
	if len(rec.Grants) != 1 || rec.Grants[0] != "net.http" {
		t.Fatalf("grants = %v, want the untouched original set", rec.Grants)
	}
}

// TestChangePermsPropagatesAnUnreadableStore covers the read branch between
// the guards and the write.
func TestChangePermsPropagatesAnUnreadableStore(t *testing.T) {
	broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
	_, err := ChangePerms(context.Background(), broken, "demo", "net.http", true, true, false)
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

// TestRemovePluginPropagatesAnUnreadableStore covers remove's read branch,
// distinct from the delete branch already covered: a remove that cannot
// read must not proceed to delete blind.
func TestRemovePluginPropagatesAnUnreadableStore(t *testing.T) {
	broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
	if _, err := RemovePlugin(context.Background(), broken, nil, "demo"); !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

// TestUpdatePluginRefusesAnUnparseableManifest covers the first gate: a
// candidate manifest that is not a manifest at all must be refused before
// any registry check or store read happens.
func TestUpdatePluginRefusesAnUnparseableManifest(t *testing.T) {
	store := storetest.NewMemStore()
	garbage := []byte("this is not a manifest = = =\x00")
	if _, err := UpdatePlugin(context.Background(), store, nil, nil, garbage, "", garbage, true); err == nil {
		t.Fatal("UpdatePlugin accepted an unparseable manifest")
	}
}

// okRegistry is a RegistryVersionChecker that succeeds, so the
// registry-reachable arm can be exercised.
type okRegistry struct{ version string }

func (r okRegistry) LatestVersion(context.Context, string) (string, error) { return r.version, nil }

// failingRegistry always reports the registry as unreachable.
type failingRegistry struct{}

func (failingRegistry) LatestVersion(context.Context, string) (string, error) {
	return "", errors.New("registry: unreachable")
}

// TestCheckRegistryGracefulCoversAllThreeArms pins the allowed-fail leg. An
// update must proceed whether the registry is absent or unreachable, and it
// must say so in its notice; a reachable registry produces no notice at
// all. Without the no-notice arm asserted, a change that returned the
// notice unconditionally would look correct — every update would just
// quietly claim the registry was unavailable.
func TestCheckRegistryGracefulCoversAllThreeArms(t *testing.T) {
	ctx := context.Background()

	if got := checkRegistryGraceful(ctx, nil, "demo"); got != registryUnavailableNotice {
		t.Errorf("nil registry produced %q, want the graceful notice", got)
	}
	if got := checkRegistryGraceful(ctx, failingRegistry{}, "demo"); got != registryUnavailableNotice {
		t.Errorf("unreachable registry produced %q, want the graceful notice", got)
	}
	if got := checkRegistryGraceful(ctx, okRegistry{version: "9.9.9"}, "demo"); got != "" {
		t.Errorf("reachable registry produced %q, want no notice", got)
	}
}

// TestUpdatePluginReportsTheRegistryNotice proves the notice reaches the
// caller's result rather than being computed and dropped.
func TestUpdatePluginReportsTheRegistryNotice(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	manifestBytes := []byte(builtinManifest)

	res, err := UpdatePlugin(ctx, store, failingRegistry{}, nil, manifestBytes, "", manifestBytes, true)
	if err != nil {
		t.Fatalf("UpdatePlugin: %v", err)
	}
	if res.RegistryNotice != registryUnavailableNotice {
		t.Errorf("RegistryNotice = %q, want the graceful notice", res.RegistryNotice)
	}

	res, err = UpdatePlugin(ctx, store, okRegistry{version: "9.9.9"}, nil, manifestBytes, "", manifestBytes, true)
	if err != nil {
		t.Fatalf("UpdatePlugin with a reachable registry: %v", err)
	}
	if res.RegistryNotice != "" {
		t.Errorf("RegistryNotice = %q for a reachable registry, want none", res.RegistryNotice)
	}
}
