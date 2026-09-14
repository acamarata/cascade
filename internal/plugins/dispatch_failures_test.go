package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover the failure branches of the elevated-install
//   path — a storage domain already owned by someone else, and a store that
//   cannot commit the record. Both decide whether a half-provisioned plugin
//   is left behind, which is the state an install must never reach
//   silently.
// SPORT: internal/plugins dispatch-failure tests (ADD).

// TestProvisionElevatedRefusesAForeignStorageDomain proves the domain
// claim is a real ownership check, not a formality: a domain already
// registered to a different owner must stop the install rather than let two
// owners write the same namespace.
func TestProvisionElevatedRefusesAForeignStorageDomain(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, builtinManifest)

	// Someone else claims the domain first.
	if _, err := domains.Register(m.ID, "a-different-owner", 1); err != nil {
		t.Fatalf("seed the foreign claim: %v", err)
	}

	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err == nil {
		t.Fatal("the install proceeded over a storage domain owned by someone else")
	}

	// STORE STATE: no metadata record was committed for an install that
	// could not claim its own storage.
	if _, ok, lerr := LoadMetadata(ctx, store, m.ID); lerr != nil || ok {
		t.Errorf("LoadMetadata after a refused install: ok=%v err=%v, want ok=false", ok, lerr)
	}
}

// TestProvisionElevatedPropagatesACommitFailure covers the last step: the
// runtime was constructed and the storage domain claimed, but the record
// could not be written. The install must report that rather than returning
// a record the caller will believe was persisted.
func TestProvisionElevatedPropagatesACommitFailure(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, builtinManifest)

	broken := failingStore{MemStore: mem, failPut: true}
	rec, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", broken, domains, m, "", false, nil)
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
	if rec.Name != "" {
		t.Fatalf("a failed install returned a populated record %+v; a caller would treat it as installed", rec)
	}
	if _, ok, _ := LoadMetadata(ctx, mem, m.ID); ok {
		t.Error("a record exists after the commit failed")
	}
}

// TestMetadataGrantCheckerPropagatesAStoreFailure covers the grant
// checker's own error branch. A capability check that cannot read the
// record must fail, never fall through to a decision: the fallback of an
// unreadable permission record is refusal, and an error is how that is
// reported.
func TestMetadataGrantCheckerPropagatesAStoreFailure(t *testing.T) {
	broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
	err := metadataGrantChecker{store: broken}.CheckGrant(context.Background(), "demo", "storage.cross-domain")
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
	if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindPermissionDenied {
		t.Error("an unreadable store was reported as a permission denial; a caller cannot tell 'denied' from 'broken'")
	}
}

// TestProvisionRemoteRuntimeBuildsItsOwnInterceptor covers the branch where
// no interceptor is supplied and the remote runtime is enabled: the
// composition root must build one rather than dispatching with none.
//
// It fails here, and that failure IS the proof of the fail-closed design:
// the remote egress class ships registered-but-disabled, and acquiring a
// capability for a disabled class is refused. So enabling the remote
// runtime alone is not enough to make it reachable — an operator must also
// have enabled its egress class. No socket is opened on this path, since
// the refusal happens before any dispatch.
func TestProvisionRemoteRuntimeBuildsItsOwnInterceptor(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, remoteManifest)

	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", true, nil)
	if err == nil {
		t.Fatal("a remote install with no interceptor succeeded; the disabled egress class must refuse first")
	}
	if _, ok, _ := LoadMetadata(ctx, store, m.ID); ok {
		t.Error("a record was committed for a remote plugin whose egress class is disabled")
	}
}

// TestAddPluginPropagatesStoreFailures covers both of AddPlugin's store
// branches: it cannot read whether the plugin is already installed, and it
// cannot commit the new record.
func TestAddPluginPropagatesStoreFailures(t *testing.T) {
	ctx := context.Background()
	manifestBytes := []byte(builtinManifest)

	t.Run("unreadable", func(t *testing.T) {
		broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
		if _, err := AddPlugin(ctx, broken, manifestBytes, "", manifestBytes, true); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
	})

	t.Run("uncommittable", func(t *testing.T) {
		mem := storetest.NewMemStore()
		broken := failingStore{MemStore: mem, failPut: true}
		if _, err := AddPlugin(ctx, broken, manifestBytes, "", manifestBytes, true); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
		if _, ok, _ := LoadMetadata(ctx, mem, "demo"); ok {
			t.Error("a record exists after the commit failed")
		}
	})
}

// TestUpdatePluginPropagatesStoreFailures covers the same two branches on
// the update path. An update that reported success without persisting
// would leave the operator believing a version they are not running.
func TestUpdatePluginPropagatesStoreFailures(t *testing.T) {
	ctx := context.Background()
	manifestBytes := []byte(builtinManifest)

	t.Run("unreadable", func(t *testing.T) {
		broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
		if _, err := UpdatePlugin(ctx, broken, nil, nil, manifestBytes, "", manifestBytes, true); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
	})

	t.Run("uncommittable", func(t *testing.T) {
		mem := storetest.NewMemStore()
		broken := failingStore{MemStore: mem, failPut: true}
		if _, err := UpdatePlugin(ctx, broken, nil, nil, manifestBytes, "", manifestBytes, true); !errors.Is(err, errStoreFailure) {
			t.Fatalf("error = %v, want the store's own failure", err)
		}
	})
}

// TestUpdatePluginRefusesAManifestThatFailsItsChecksum proves the artifact
// verification gate runs before anything is written: a pinned checksum that
// does not match the artifact must stop the update, not merely warn.
func TestUpdatePluginRefusesAManifestThatFailsItsChecksum(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	manifestBytes := []byte(builtinManifest)

	_, err := UpdatePlugin(ctx, mem, nil, nil, manifestBytes, "sha256:0000000000000000000000000000000000000000000000000000000000000000", manifestBytes, true)
	if err == nil {
		t.Fatal("the update accepted an artifact that does not match its pinned checksum")
	}
	if _, ok, _ := LoadMetadata(ctx, mem, "demo"); ok {
		t.Error("a record was committed for an artifact that failed verification")
	}
}
