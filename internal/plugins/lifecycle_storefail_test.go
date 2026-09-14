package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): drive the store-failure branch of every metadata
//   operation. These paths decide what happens when the plugin database is
//   unreadable or unwritable, and an operation that swallowed such an error
//   would report success while the record it claimed to write does not
//   exist — the worst possible outcome for an install/permission record.
// SPORT: internal/plugins store-failure tests (ADD).

// errStoreFailure is the sentinel every fake below returns, so a test can
// assert the ORIGINAL error reached the caller rather than some
// substituted one that merely happens to be non-nil.
var errStoreFailure = errors.New("store: simulated backend failure")

// failingStore wraps a real MemStore and fails exactly one method. Wrapping
// rather than reimplementing means every other operation behaves normally,
// so a test exercises the real code path right up to the failure point.
type failingStore struct {
	*storetest.MemStore
	failGet    bool
	failPut    bool
	failDelete bool
	failScan   bool
}

func (f failingStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if f.failGet {
		return nil, errStoreFailure
	}
	return f.MemStore.Get(ctx, namespace, key)
}

func (f failingStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if f.failPut {
		return errStoreFailure
	}
	return f.MemStore.Put(ctx, namespace, key, value)
}

func (f failingStore) Delete(ctx context.Context, namespace, key string) error {
	if f.failDelete {
		return errStoreFailure
	}
	return f.MemStore.Delete(ctx, namespace, key)
}

func (f failingStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if f.failScan {
		return nil, errStoreFailure
	}
	return f.MemStore.Scan(ctx, namespace, prefix)
}

// seededFailingStore returns a store holding one installed plugin record,
// with the named method armed to fail. Seeding happens before arming, so
// the fixture is always consistent.
func seededFailingStore(t *testing.T, arm func(*failingStore)) failingStore {
	t.Helper()
	mem := storetest.NewMemStore()
	if err := SaveMetadata(context.Background(), mem, PluginMetadata{
		Name: "seeded", Enabled: true, InstalledVersion: "1.0.0", Grants: []string{"net.http"},
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	fs := failingStore{MemStore: mem}
	arm(&fs)
	return fs
}

func TestLoadMetadataPropagatesAStoreFailure(t *testing.T) {
	store := seededFailingStore(t, func(f *failingStore) { f.failGet = true })
	_, _, err := LoadMetadata(context.Background(), store, "seeded")
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

// TestLoadMetadataDistinguishesAbsentFromBroken is the assertion that makes
// the one above matter: "not installed" and "the database is unreadable"
// must not collapse into the same answer, or a transient backend failure
// reads as an uninstalled plugin and the caller reinstalls over it.
func TestLoadMetadataDistinguishesAbsentFromBroken(t *testing.T) {
	mem := storetest.NewMemStore()
	_, ok, err := LoadMetadata(context.Background(), mem, "never-installed")
	if err != nil {
		t.Fatalf("absent record reported an error: %v", err)
	}
	if ok {
		t.Fatal("absent record reported ok=true")
	}

	broken := failingStore{MemStore: mem, failGet: true}
	_, ok, err = LoadMetadata(context.Background(), broken, "never-installed")
	if err == nil {
		t.Fatal("an unreadable store reported no error; it is indistinguishable from 'not installed'")
	}
	if ok {
		t.Fatal("an unreadable store reported ok=true")
	}
}

func TestSaveMetadataPropagatesAStoreFailure(t *testing.T) {
	store := failingStore{MemStore: storetest.NewMemStore(), failPut: true}
	err := SaveMetadata(context.Background(), store, PluginMetadata{Name: "p", Enabled: true})
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

func TestListMetadataPropagatesAStoreFailure(t *testing.T) {
	store := seededFailingStore(t, func(f *failingStore) { f.failScan = true })
	_, err := ListMetadata(context.Background(), store)
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

func TestSetEnabledPropagatesAStoreFailure(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		store := seededFailingStore(t, func(f *failingStore) { f.failGet = true })
		if _, err := SetEnabled(context.Background(), store, "seeded", false); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
	})
	t.Run("unwritable", func(t *testing.T) {
		store := seededFailingStore(t, func(f *failingStore) { f.failPut = true })
		if _, err := SetEnabled(context.Background(), store, "seeded", false); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
	})
}

// TestSetEnabledDoesNotReportSuccessOnAFailedWrite is the state assertion
// behind the error assertion: an unwritable store must leave the record as
// it was, never report a flag it did not persist.
func TestSetEnabledDoesNotReportSuccessOnAFailedWrite(t *testing.T) {
	mem := storetest.NewMemStore()
	if err := SaveMetadata(context.Background(), mem, PluginMetadata{
		Name: "seeded", Enabled: true, InstalledVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	broken := failingStore{MemStore: mem, failPut: true}
	if _, err := SetEnabled(context.Background(), broken, "seeded", false); err == nil {
		t.Fatal("SetEnabled reported success against an unwritable store")
	}
	rec, ok, err := LoadMetadata(context.Background(), mem, "seeded")
	if err != nil || !ok {
		t.Fatalf("re-reading the record: ok=%v err=%v", ok, err)
	}
	if !rec.Enabled {
		t.Fatal("the failed write still flipped the stored record")
	}
}

func TestChangePermsPropagatesAStoreFailure(t *testing.T) {
	store := seededFailingStore(t, func(f *failingStore) { f.failPut = true })
	// daemonAvailable=true, noInput=false: both elevation gates satisfied, so
	// the call reaches the store and this asserts the STORE failure rather
	// than a refusal that never got that far.
	_, err := ChangePerms(context.Background(), store, "seeded", "storage.read", true, true, false)
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

func TestRemovePluginPropagatesAStoreFailure(t *testing.T) {
	store := seededFailingStore(t, func(f *failingStore) { f.failDelete = true })
	if _, err := RemovePlugin(context.Background(), store, nil, "seeded"); !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}
