package runtime

// Purpose: unit coverage for StoreDomainRegistry (domain_registry.go),
//   split under the same-file-cap-driven layout as the production code it
//   tests. Exercises OrphanedLocks/Release directly against a real
//   provider.Store (storetest.MemStore: a real reference implementation,
//   not a stub, per its own doc comment), independent of Scan's own
//   liveness classification.
// SPORT: runtime/domain_registry (ADD).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

func TestStoreDomainRegistry_OrphanedLocks_EmptyLedgerIsNotAnError(t *testing.T) {
	reg := NewStoreDomainRegistry(storetest.NewMemStore())
	locks, err := reg.OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks on an empty ledger: %v", err)
	}
	if len(locks) != 0 {
		t.Fatalf("locks = %+v, want empty", locks)
	}
}

func TestStoreDomainRegistry_OrphanedLocks_ReturnsSeededRecords(t *testing.T) {
	store := storetest.NewMemStore()
	seedLedger(t, store, []domainLockRecord{
		{LockID: "lock-a", OwnerPID: 111},
		{LockID: "lock-b", OwnerPID: 222},
	})

	reg := NewStoreDomainRegistry(store)
	locks, err := reg.OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks: %v", err)
	}
	if len(locks) != 2 {
		t.Fatalf("locks = %+v, want 2 entries", locks)
	}
	if locks[0].LockID != "lock-a" || locks[0].OwnerPID != 111 {
		t.Errorf("locks[0] = %+v, want lock-a/111", locks[0])
	}
	if locks[1].LockID != "lock-b" || locks[1].OwnerPID != 222 {
		t.Errorf("locks[1] = %+v, want lock-b/222", locks[1])
	}
}

func TestStoreDomainRegistry_Release_RemovesOnlyTheNamedLock(t *testing.T) {
	store := storetest.NewMemStore()
	seedLedger(t, store, []domainLockRecord{
		{LockID: "keep-me", OwnerPID: 1},
		{LockID: "release-me", OwnerPID: 2},
	})

	reg := NewStoreDomainRegistry(store)
	if err := reg.Release(context.Background(), "release-me"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	locks, err := reg.OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks after Release: %v", err)
	}
	if len(locks) != 1 || locks[0].LockID != "keep-me" {
		t.Fatalf("locks after Release = %+v, want only keep-me", locks)
	}
}

func TestStoreDomainRegistry_RegisterLock_ThenOrphanedLocksSeesIt(t *testing.T) {
	reg := NewStoreDomainRegistry(storetest.NewMemStore())
	if err := reg.RegisterLock(context.Background(), "new-lock", 4242); err != nil {
		t.Fatalf("RegisterLock: %v", err)
	}
	locks, err := reg.OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks: %v", err)
	}
	if len(locks) != 1 || locks[0].LockID != "new-lock" || locks[0].OwnerPID != 4242 {
		t.Fatalf("locks = %+v, want [{new-lock 4242}]", locks)
	}
}

func TestStoreDomainRegistry_RegisterLock_ReplacesExistingOwner(t *testing.T) {
	reg := NewStoreDomainRegistry(storetest.NewMemStore())
	if err := reg.RegisterLock(context.Background(), "lock", 1); err != nil {
		t.Fatalf("RegisterLock (1st): %v", err)
	}
	if err := reg.RegisterLock(context.Background(), "lock", 2); err != nil {
		t.Fatalf("RegisterLock (2nd): %v", err)
	}
	locks, err := reg.OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks: %v", err)
	}
	if len(locks) != 1 || locks[0].OwnerPID != 2 {
		t.Fatalf("locks = %+v, want a single entry with OwnerPID=2", locks)
	}
}

func TestStoreDomainRegistry_Release_AbsentLockIDIsNoop(t *testing.T) {
	reg := NewStoreDomainRegistry(storetest.NewMemStore())
	if err := reg.Release(context.Background(), "never-existed"); err != nil {
		t.Fatalf("Release on an absent lock: %v, want nil (no-op)", err)
	}
}

func TestStoreDomainRegistry_OrphanedLocks_CorruptLedgerIsTypedError(t *testing.T) {
	store := storetest.NewMemStore()
	if err := store.Put(context.Background(), storeRegistryNamespace, storeRegistryKey, []byte("not json")); err != nil {
		t.Fatalf("seed corrupt ledger: %v", err)
	}
	reg := NewStoreDomainRegistry(store)
	if _, err := reg.OrphanedLocks(context.Background()); err == nil {
		t.Fatal("OrphanedLocks over a corrupt ledger: want an error, got nil")
	}
}

// seedLedger writes recs directly into store as StoreDomainRegistry's own
// JSON ledger shape, bypassing Release/registration entirely — this is
// the seam a real crash-recovery test also uses: a lock left behind by a
// killed process is exactly this, bytes already on disk, no live writer.
func seedLedger(t *testing.T, store interface {
	Put(ctx context.Context, namespace, key string, value []byte) error
}, recs []domainLockRecord) {
	t.Helper()
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatalf("marshal seed ledger: %v", err)
	}
	if err := store.Put(context.Background(), storeRegistryNamespace, storeRegistryKey, data); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}
