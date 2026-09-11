package jobs

// Purpose: the tx-scoped SQL helpers Acquire/Release (lease.go), Renew/
//
//	the expiry sweep (lease_expiry.go) and Fence/reclaim (lease_fence.go)
//	all share: listing a repo's leases, reading/writing one lease row
//	inside an already-open *sql.Tx, computing the next epoch for a
//	scope, and finding every lease a given holder currently has. None of
//	these duplicate store_lease.go's PutLease/GetLease (which open their
//	OWN implicit connection checkout) -- every lease mutation in this
//	ticket runs inside Store.withTx's single transaction (lease.go), so
//	these read/write through the *sql.Tx the caller already holds.
//
// Inputs: an open *sql.Tx (or, for allLeasesForHolder, the Store's db
//
//	directly -- a read-only listing outside any mutation).
//
// Outputs: ResourceLease values, or a typed A-T7 error.
// Constraints: nextEpochTx computes MAX(epoch)+1 over every row EVER
//
//	recorded for (repoID, scope) -- including released/expired rows --
//	so the epoch never resets even after a scope's lease is fully
//	released and re-acquired (R-21.139 monotonicity survives release).
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// withTx runs fn inside one transaction over the Store's db. Every
// Acquire/Release/Fence/sweep mutation goes through this single seam so
// R-21.169's "single write-executor" serialization is enforced in
// exactly one place: the Store's db is opened with SetMaxOpenConns(1)
// (migration_test.go's openTestDB; the production composition root's
// equivalent), so a second concurrent withTx call blocks in BeginTx
// until this one commits or rolls back -- there is no window in which
// two transactions can each see the pre-grant state.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: begin lease transaction")
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: commit lease transaction")
	}
	return nil
}

// leaseEntityID names the journal/event entity for one (repoID,
// normalizedScope) lease, stable across its whole acquire/renew/expire/
// release lifecycle and every epoch it passes through.
func leaseEntityID(repoID, normalizedScope string) string {
	return "lease:" + repoID + ":" + normalizedScope
}

// listLeasesTx returns every resource_lease row for repoID, in the
// transaction tx already holds.
func listLeasesTx(ctx context.Context, tx *sql.Tx, repoID string) ([]ResourceLease, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list leases")
	}
	defer func() { _ = rows.Close() }()
	var out []ResourceLease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate leases")
	}
	return out, nil
}

// leaseScanner is the subset of *sql.Row/*sql.Rows scanLease needs.
type leaseScanner interface {
	Scan(dest ...any) error
}

func scanLease(row leaseScanner) (ResourceLease, error) {
	var l ResourceLease
	var state string
	if err := row.Scan(&l.RepoID, &l.ScopeGlob, &l.Holder, &l.IssuedAt, &l.TTLSeconds,
		&l.RenewCount, &l.JournalRef, &l.Epoch, &state); err != nil {
		return ResourceLease{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan lease")
	}
	var err error
	if l.State, err = DecodeLeaseState(state); err != nil {
		return ResourceLease{}, err
	}
	return l, nil
}

// getLeaseTx reads one lease row inside tx.
func getLeaseTx(ctx context.Context, tx *sql.Tx, repoID, scopeGlob string) (ResourceLease, bool, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE repo_id = ? AND scope_glob = ?`, repoID, scopeGlob)
	l, err := scanLease(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResourceLease{}, false, nil
		}
		return ResourceLease{}, false, err
	}
	return l, true, nil
}

// putLeaseTx writes lease inside tx (insert-or-replace, mirroring
// store_lease.go's PutLease upsert, minus that function's own connection
// checkout).
func putLeaseTx(ctx context.Context, tx *sql.Tx, l ResourceLease) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableResourceLease+` (repo_id, scope_glob, holder, issued_at, ttl_seconds,
			renew_count, journal_ref, epoch, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo_id, scope_glob) DO UPDATE SET holder=excluded.holder,
			issued_at=excluded.issued_at, ttl_seconds=excluded.ttl_seconds,
			renew_count=excluded.renew_count, journal_ref=excluded.journal_ref,
			epoch=excluded.epoch, state=excluded.state`,
		l.RepoID, l.ScopeGlob, l.Holder, l.IssuedAt, l.TTLSeconds, l.RenewCount,
		l.JournalRef, l.Epoch, string(l.State))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put lease (tx)")
	}
	return nil
}

// nextEpochTx returns one greater than the highest epoch ever recorded
// for (repoID, scope) -- including released/expired rows, so epoch
// monotonicity survives a full release-and-reacquire cycle (R-21.139).
// 1 for a scope with no prior row.
func nextEpochTx(ctx context.Context, tx *sql.Tx, repoID, scope string) (int64, error) {
	var maxEpoch sql.NullInt64
	row := tx.QueryRowContext(ctx,
		`SELECT MAX(epoch) FROM `+tableResourceLease+` WHERE repo_id = ? AND scope_glob = ?`, repoID, scope)
	if err := row.Scan(&maxEpoch); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read max epoch")
	}
	if !maxEpoch.Valid {
		return 1, nil
	}
	return maxEpoch.Int64 + 1, nil
}

// allLeasesForHolder returns every lease row currently naming holder,
// across every repo. Read-only; used by releaseAllForJob and by tests
// asserting release-on-terminal.
func (s *Store) allLeasesForHolder(ctx context.Context, holder string) ([]ResourceLease, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE holder = ?`, holder)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list leases by holder")
	}
	defer func() { _ = rows.Close() }()
	var out []ResourceLease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate leases by holder")
	}
	return out, nil
}

// heldOrRenewingTx lists every held/renewing lease row inside tx --
// SweepExpired's candidate set (lease_expiry.go's funlen-cap split).
func heldOrRenewingTx(ctx context.Context, tx *sql.Tx) ([]ResourceLease, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE state IN (?, ?)`, string(LeaseHeld), string(LeaseRenewing))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list held leases for sweep")
	}
	defer func() { _ = rows.Close() }()
	var out []ResourceLease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate held leases for sweep")
	}
	return out, nil
}

// acquireInTx is Acquire's transaction body (lease.go's funlen-cap
// split): the conflict scan against every contending row in repoID,
// then -- only on no conflict -- the epoch-stamped grant.
func (m *LeaseManager) acquireInTx(ctx context.Context, tx *sql.Tx, repoID, normalized, holder string, scope Scope) (AcquireResult, error) {
	existing, err := listLeasesTx(ctx, tx, repoID)
	if err != nil {
		return AcquireResult{}, err
	}
	for _, other := range existing {
		if !other.State.Contending() {
			continue
		}
		otherScope, err := ParseScope(other.ScopeGlob)
		if err != nil {
			return AcquireResult{}, err
		}
		if scope.Intersects(otherScope) {
			return AcquireResult{Granted: false, Contending: other}, nil
		}
	}
	nextEpoch, err := nextEpochTx(ctx, tx, repoID, normalized)
	if err != nil {
		return AcquireResult{}, err
	}
	lease := ResourceLease{
		RepoID: repoID, ScopeGlob: normalized, Holder: holder,
		IssuedAt: m.clock.Now().Unix(), TTLSeconds: m.defaults.TTLSeconds,
		RenewCount: 0, JournalRef: leaseEntityID(repoID, normalized),
		Epoch: nextEpoch, State: LeaseHeld,
	}
	if err := putLeaseTx(ctx, tx, lease); err != nil {
		return AcquireResult{}, err
	}
	return AcquireResult{Granted: true, Lease: lease}, nil
}
