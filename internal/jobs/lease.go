package jobs

// Purpose: LeaseManager -- the DECIDED "ONE MUTABLE WRITER PER LEASE
//
//	SCOPE" authority over the S-59.T1 resource_lease table: Acquire and
//	the explicit Release path, plus LeaseDefaults (the R-16.37 [jobs].
//	lease constants) and the controller-singleton guard (R-21.169) both
//	Acquire and Release share with lease_expiry.go's sweep. Renew and
//	the expiry sweep are lease_expiry.go's (300-line-cap split); the
//	fence/state-machine/reclaim machinery is lease_fence.go's; event and
//	attention-queue integration is lease_events.go's.
//
// Inputs: a *Store (this package's S-59.T1 persistence), an injected
//
//	runtime.Clock, and (for Release's terminal-state co-ownership path)
//	nothing extra -- store_job.go calls back into Release itself.
//
// Outputs: a granted lease, a typed contended result naming the
//
//	conflicting lease, or a typed error (KindPermissionDenied for a
//	non-controller caller, KindConflict for a fenced/illegal mutation).
//
// Constraints: the conflict check and the grant serialize through ONE
//
//	sql.Tx over the Store's single-connection *sql.DB (db.SetMaxOpenConns
//	(1), set by every caller of NewStore per this ticket's testdata/
//	README.md provenance) -- R-21.169's "single write-executor" is this
//	repo's existing one-connection convention, not a new lock this
//	ticket invents. Acquire, Release and the lease_expiry.go sweep all
//	refuse (ErrNotController) unless isController reports true.
//
// SPORT: jobs/lease-model + jobs/glob-intersection (ADD, P1-E29-W6-S59-T2).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LeaseDefaults is the R-16.37 `[jobs].lease` constant set, injected by
// the daemon composition root. DefaultLeaseDefaults returns the verbatim
// values; a caller that wants different values (tests, or a future
// config-driven override) constructs its own LeaseDefaults directly --
// there is no hidden global.
//
// CONTRACT NOTE (files_scope, recorded per R-16.79 rather than papered
// over): this ticket's full_desc asks that internal/runtime/config
// register the `[jobs].lease` keys (08-INIT-CONFIG-SPEC §3). This
// ticket's own files_scope change-list names only go.mod/go.sum/
// store_job.go/testdata/README.md/the wiki page -- no file under
// internal/runtime/config is listed, and that subsystem's own section
// parsers (config_sections.go, config_load.go, the per-section files
// each with their own schema doc and tests) are a materially different
// piece of work than this ticket's lease model. Registering a new
// section there blind, with no files_scope coverage, risks colliding
// with whichever ticket DOES own that surface. This ticket therefore
// ships LeaseDefaults as the typed, injectable value config wiring
// needs to hand the composition root, and defers the actual
// internal/runtime/config registration as a named follow-up rather than
// guessing at an out-of-scope subsystem's shape.
type LeaseDefaults struct {
	TTLSeconds         int64
	RenewEverySeconds  int64
	ExpiryGraceSeconds int64
}

// DefaultLeaseDefaults returns the R-16.37 verbatim values: ttl=2h,
// renew_every=15m, expiry_grace=10m.
func DefaultLeaseDefaults() LeaseDefaults {
	return LeaseDefaults{
		TTLSeconds:         2 * 60 * 60,
		RenewEverySeconds:  15 * 60,
		ExpiryGraceSeconds: 10 * 60,
	}
}

// ErrNotController is returned by Acquire, Release and the expiry sweep
// when isController reports false (R-21.169): a node daemon reaching
// lease.acquire or lease.release refuses outright. There is no
// elevation path being offered, matching KindPermissionDenied's own
// documented meaning.
var ErrNotController = cascade.New(cascade.KindPermissionDenied,
	"jobs: lease acquire/release runs only on the controller daemon")

// LeaseManager is the DECIDED one-mutable-writer authority over Store's
// resource_lease table. The zero value is not usable; construct with
// NewLeaseManager.
type LeaseManager struct {
	store        *Store
	clock        runtime.Clock
	isController func() bool
	defaults     LeaseDefaults
	sink         *leaseEventSink // nil is legitimate: no journal/bus/attention wiring (tests)
}

// NewLeaseManager constructs a LeaseManager. isController reports
// whether THIS daemon process holds the B/S-02.T2 database advisory
// lock -- pass a func that always returns true for a single-daemon test
// or a controller-only production process. sink may be nil (no
// journal/event-bus/attention integration); see lease_events.go.
func NewLeaseManager(store *Store, clock runtime.Clock, isController func() bool, defaults LeaseDefaults, sink *leaseEventSink) *LeaseManager {
	return &LeaseManager{store: store, clock: clock, isController: isController, defaults: defaults, sink: sink}
}

// AcquireResult is Acquire's outcome: exactly one of Lease (granted) or
// Contended (refused, naming the conflicting lease) is populated.
type AcquireResult struct {
	Granted    bool
	Lease      ResourceLease
	Contending ResourceLease // valid iff !Granted
}

// Acquire attempts to grant holder exclusive use of scopeGlob within
// repoID. The conflict check and the grant happen inside one
// transaction over the Store's single-connection db, so two concurrent
// Acquire calls for intersecting scopes can never both observe "no
// conflict" -- exactly one commits a `held` row; the other's SELECT (run
// after the first's commit, since both share one connection) sees it and
// returns Contended.
func (m *LeaseManager) Acquire(ctx context.Context, repoID, scopeGlob, holder string) (AcquireResult, error) {
	if !m.isController() {
		return AcquireResult{}, ErrNotController
	}
	if repoID == "" || holder == "" {
		return AcquireResult{}, cascade.New(cascade.KindInvalidInput, "jobs: lease acquire requires repo id and holder")
	}
	scope, err := NormalizeScope(scopeGlob)
	if err != nil {
		return AcquireResult{}, err
	}
	normalized := scope.String()

	var result AcquireResult
	txErr := m.store.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = m.acquireInTx(ctx, tx, repoID, normalized, holder, scope)
		return err
	})
	if txErr != nil {
		return AcquireResult{}, txErr
	}
	if result.Granted && m.sink != nil {
		if err := m.sink.acquired(ctx, result.Lease); err != nil {
			return result, err
		}
	}
	if !result.Granted && m.sink != nil {
		if err := m.sink.contended(ctx, repoID, normalized, holder, result.Contending); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Release explicitly releases the lease at (repoID, scopeGlob), refusing
// with ErrLeaseFenced (lease_fence.go) if epoch does not match the
// stored row's current epoch -- the same fence check every lease-scoped
// mutation presents, per R-21.193's stall-release co-ownership contract.
// Two callers use this: an explicit caller-initiated release, and
// store_job.go's job-terminal-transition hook (releaseAllForJob).
func (m *LeaseManager) Release(ctx context.Context, repoID, scopeGlob string, epoch int64) error {
	if !m.isController() {
		return ErrNotController
	}
	var released ResourceLease
	var did bool
	txErr := m.store.withTx(ctx, func(tx *sql.Tx) error {
		lease, ok, err := getLeaseTx(ctx, tx, repoID, scopeGlob)
		if err != nil {
			return err
		}
		if !ok || lease.State == LeaseReleased {
			return nil // already released, or never existed: a no-op, not an error
		}
		if lease.Epoch != epoch {
			return cascade.Wrapf(cascade.KindConflict, ErrLeaseFenced,
				"jobs: release %s/%s at epoch %d refused", repoID, scopeGlob, epoch)
		}
		lease.State = LeaseReleased
		if err := putLeaseTx(ctx, tx, lease); err != nil {
			return err
		}
		released, did = lease, true
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if did && m.sink != nil {
		return m.sink.released(ctx, released)
	}
	return nil
}

// releaseAllForJob releases every lease jobID currently holds, in the
// closed non-released states. Called by store_job.go's PutTransition
// once a transition lands jobID in a terminal state (R-21.193/DECIDED
// release-on-terminal path).
func (m *LeaseManager) releaseAllForJob(ctx context.Context, jobID string) error {
	if !m.isController() {
		return ErrNotController
	}
	leases, err := m.store.allLeasesForHolder(ctx, jobID)
	if err != nil {
		return err
	}
	for _, l := range leases {
		if l.State == LeaseReleased {
			continue
		}
		if err := m.Release(ctx, l.RepoID, l.ScopeGlob, l.Epoch); err != nil && !errors.Is(err, ErrNotController) {
			return err
		}
	}
	return nil
}

// Contending reports whether a lease in this state blocks a new,
// intersecting Acquire: held or renewing (step 11's closed state
// machine). expired_unconfirmed ALSO blocks (no contender is admitted
// until confirmed termination, R-21.139) -- lease_fence.go's reclaim
// path is what moves a row out of that state.
func (s LeaseState) Contending() bool {
	switch s {
	case LeaseHeld, LeaseRenewing, LeaseExpiredUnconfirmed:
		return true
	case LeaseExpiredOrphaned, LeaseReleased:
		return false
	}
	return false
}

// WireLeaseRelease installs m's release-on-terminal path as store's
// PutTransition hook (store_job.go's onTerminal): the composition root
// calls this once, after constructing both store and m, so that a job
// terminal-state transition releases every lease that job holds
// (DECIDED release path 2). Idempotent to call more than once.
func WireLeaseRelease(store *Store, m *LeaseManager) {
	store.SetTerminalHook(m.releaseAllForJob)
}
