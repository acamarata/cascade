package runtime

// Purpose: StoreDomainRegistry, the real DomainRegistry implementation
//   BootstrapOptions.RecoveryRegistry needs at a production call site.
//   Split out of bootstrap.go to stay under the 300-line file cap.
// Inputs: a provider.Store the composition root constructs once and
//   shares across every subsystem that needs one.
// Outputs: OrphanedLocks/Release satisfying the DomainRegistry interface
//   recovery.go declares.
// Constraints: no bare time.Now (there is none here; PID liveness and
//   timestamps stay Scan's job, per DomainRegistry's own doc). Every
//   Store error is re-wrapped through pkg/cascade's taxonomy, never
//   returned raw.
// SPORT: runtime/domain_registry (ADD).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// domainLockRecord is one StoreDomainRegistry ledger entry's wire shape.
type domainLockRecord struct {
	LockID   string `json:"lock_id"`
	OwnerPID int    `json:"owner_pid"`
}

// storeRegistryNamespace/Key locate StoreDomainRegistry's ledger: a single
// JSON-encoded array of domainLockRecord, one record per outstanding
// advisory lock. A single key is enough at this scale (crash-recovery
// cleanup is not a hot path) and keeps Release's read-modify-write a
// single Tx (Store.Tx already gives atomicity, so no CompareAndSwap is
// needed on top of it).
const (
	storeRegistryNamespace = "runtime"
	storeRegistryKey       = "advisory_locks"
)

// StoreDomainRegistry is the real DomainRegistry implementation this
// package's own doc comment (recovery.go) named as the missing seam: "a
// future PID-keyed, Store-persisted registry". No such registry existed
// anywhere in the tree before this one. internal/events/scheduler's
// advisory lock (lock.go) is lease/TTL-based with a string owner ID, not a
// PID, is unexported, and is structurally unreachable from here (the
// import edge runs internal/events may import internal/runtime, never the
// reverse). The
// in-process capability registry providers/sqlite.Driver carries privately
// dies with the process, so a post-crash scanner would find nothing there
// even if it could reach it (recovery.go's DomainRegistry doc makes the
// same point about that registry). StoreDomainRegistry closes the gap: a
// provider.Store-backed ledger of lock_id/owner_pid entries, so a lock
// left behind by a killed process is still on disk for the NEXT process's
// Scan to find, exactly as the crash-recovery contract requires.
//
// It is deliberately independent of the scheduler's own lease lock. That
// lock self-heals by TTL and needs no post-crash sweep at all (see
// recovery.go's package doc, LOCKS-CONSIDERED item 2). StoreDomainRegistry
// exists for future domains that need pid-keyed, kill(0)-verified cleanup
// rather than TTL expiry. It starts with an empty ledger in production
// until such a domain registers a lock through it, an honest starting
// state, not a fake pass (Scan already treats "no records" as "nothing to
// clean", never as a signal the seam itself is unreachable).
type StoreDomainRegistry struct {
	store provider.Store
}

// NewStoreDomainRegistry builds a StoreDomainRegistry over store. The
// composition root (cmd/cascade/daemon_unix.go) constructs store once
// (the real on-disk cascade.db) and shares it across every subsystem that
// needs one, including this registry.
func NewStoreDomainRegistry(store provider.Store) *StoreDomainRegistry {
	return &StoreDomainRegistry{store: store}
}

// OrphanedLocks implements DomainRegistry. A ledger that has never been
// written (KindNotFound) reports zero candidates, not an error. This is
// the ordinary "nothing has ever registered a lock" state.
func (r *StoreDomainRegistry) OrphanedLocks(ctx context.Context) ([]OrphanedLock, error) {
	recs, err := r.readLedger(ctx, r.store)
	if err != nil {
		return nil, err
	}
	out := make([]OrphanedLock, 0, len(recs))
	for _, rec := range recs {
		out = append(out, OrphanedLock(rec))
	}
	return out, nil
}

// RegisterLock records a lock owned by ownerPID under lockID, the write
// half of the CRUD set DomainRegistry's read half (OrphanedLocks) and
// cleanup half (Release) complete: a future domain that needs pid-keyed,
// kill(0)-verified lock cleanup (rather than the scheduler's TTL-based
// lease) calls this when it acquires the lock, so a crash leaves the
// record for the NEXT process's Scan to find. Registering a lockID that
// already exists replaces its owner PID rather than adding a duplicate
// entry.
func (r *StoreDomainRegistry) RegisterLock(ctx context.Context, lockID string, ownerPID int) error {
	return r.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		recs, err := r.readLedger(ctx, tx)
		if err != nil {
			return err
		}
		found := false
		for i, rec := range recs {
			if rec.LockID == lockID {
				recs[i].OwnerPID = ownerPID
				found = true
				break
			}
		}
		if !found {
			recs = append(recs, domainLockRecord{LockID: lockID, OwnerPID: ownerPID})
		}
		data, merr := json.Marshal(recs)
		if merr != nil {
			return cascade.Wrap(cascade.KindInternal, merr, "runtime: encode advisory lock ledger")
		}
		return tx.Put(ctx, storeRegistryNamespace, storeRegistryKey, data)
	})
}

// Release implements DomainRegistry: removes lockID from the ledger inside
// one Store.Tx, so a concurrent Release/registration never observes a
// half-written ledger. Releasing an already-absent lockID is a no-op, not
// an error, mirroring scheduler's own advisoryLock.Release contract.
func (r *StoreDomainRegistry) Release(ctx context.Context, lockID string) error {
	return r.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		recs, err := r.readLedger(ctx, tx)
		if err != nil {
			return err
		}
		kept := make([]domainLockRecord, 0, len(recs))
		for _, rec := range recs {
			if rec.LockID != lockID {
				kept = append(kept, rec)
			}
		}
		data, merr := json.Marshal(kept)
		if merr != nil {
			return cascade.Wrap(cascade.KindInternal, merr, "runtime: encode advisory lock ledger")
		}
		return tx.Put(ctx, storeRegistryNamespace, storeRegistryKey, data)
	})
}

// ledgerReader is the Get-only subset Store and Tx both satisfy, so
// readLedger works identically inside or outside a transaction.
type ledgerReader interface {
	Get(ctx context.Context, namespace, key string) ([]byte, error)
}

// readLedger reads and decodes the ledger via reader (a Store or a Tx). A
// missing key decodes as an empty ledger, never an error.
func (r *StoreDomainRegistry) readLedger(ctx context.Context, reader ledgerReader) ([]domainLockRecord, error) {
	data, err := reader.Get(ctx, storeRegistryNamespace, storeRegistryKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "runtime: read advisory lock ledger")
	}
	var recs []domainLockRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "runtime: corrupt advisory lock ledger")
	}
	return recs, nil
}
