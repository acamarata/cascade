package jobs

// Purpose: typed CRUD for resource_lease and worktree. PutLease
//
//	store-enforces R-21.139's epoch monotonicity (an UPDATE that would
//	lower epoch is refused with a typed error) and validates the closed
//	LeaseState vocabulary. PutWorktree store-enforces the binding to an
//	existing resource_lease row -- the composite-FK limitation
//	migration.go's doc comment records.
//
// Inputs: an open *sql.DB already migrated via ApplyJobsSchema.
// Outputs: typed A-T7 errors; KindConflict for the epoch-lowering and
//
//	missing-lease-binding paths.
//
// Constraints: the lease MODEL here is the DECIDED record plus the W6
//
//	fencing columns only -- the fence CHECK on every mutation is
//	AC/S-59.T2's, the pgid liveness probe and quarantine sweep are
//	AC/S-59.T3's; this file never calls either.
//
// SPORT: jobs/store/ADD (P1-E29-W6-S59-T1).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PutLease inserts a new lease row, or updates an existing one keyed by
// (RepoID, ScopeGlob). Refuses (typed KindConflict) any update whose
// Epoch is lower than the stored row's -- R-21.139's monotonicity.
func (s *Store) PutLease(ctx context.Context, l ResourceLease) error {
	if l.RepoID == "" || l.ScopeGlob == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: resource_lease repo_id and scope_glob are required")
	}
	if !l.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown lease state %q", string(l.State))
	}
	existing, ok, err := s.GetLease(ctx, l.RepoID, l.ScopeGlob)
	if err != nil {
		return err
	}
	if ok && l.Epoch < existing.Epoch {
		return cascade.Newf(cascade.KindConflict,
			"jobs: resource_lease (%s, %s) epoch may not decrease: stored %d, refusing %d",
			l.RepoID, l.ScopeGlob, existing.Epoch, l.Epoch)
	}
	_, err = s.db.ExecContext(ctx,
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
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put lease")
	}
	return nil
}

// GetLease reads one resource_lease row by its (RepoID, ScopeGlob)
// natural key.
func (s *Store) GetLease(ctx context.Context, repoID, scopeGlob string) (ResourceLease, bool, error) {
	var l ResourceLease
	var state string
	row := s.db.QueryRowContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE repo_id = ? AND scope_glob = ?`, repoID, scopeGlob)
	err := row.Scan(&l.RepoID, &l.ScopeGlob, &l.Holder, &l.IssuedAt, &l.TTLSeconds,
		&l.RenewCount, &l.JournalRef, &l.Epoch, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ResourceLease{}, false, nil
	}
	if err != nil {
		return ResourceLease{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get lease")
	}
	if l.State, err = DecodeLeaseState(state); err != nil {
		return ResourceLease{}, false, err
	}
	return l, true, nil
}

// PutWorktree inserts or replaces one worktree row. Refuses (typed
// KindConflict) when (LeaseRepoID, LeaseScopeGlob) does not reference an
// existing resource_lease row -- the application-layer binding check
// migration.go's COMPOSITE-FK LIMITATION doc comment describes.
func (s *Store) PutWorktree(ctx context.Context, w Worktree) error {
	if w.Path == "" || w.LeaseRepoID == "" || w.LeaseScopeGlob == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: worktree path, lease_repo_id and lease_scope_glob are required")
	}
	_, ok, err := s.GetLease(ctx, w.LeaseRepoID, w.LeaseScopeGlob)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindConflict,
			"jobs: worktree %q references missing resource_lease (%s, %s)", w.Path, w.LeaseRepoID, w.LeaseScopeGlob)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableWorktree+` (path, lease_repo_id, lease_scope_glob, repo, branch)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET lease_repo_id=excluded.lease_repo_id,
			lease_scope_glob=excluded.lease_scope_glob, repo=excluded.repo, branch=excluded.branch`,
		w.Path, w.LeaseRepoID, w.LeaseScopeGlob, w.Repo, w.Branch)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put worktree")
	}
	return nil
}

// GetWorktree reads one worktree row by its path.
func (s *Store) GetWorktree(ctx context.Context, path string) (Worktree, bool, error) {
	var w Worktree
	row := s.db.QueryRowContext(ctx,
		`SELECT path, lease_repo_id, lease_scope_glob, repo, branch FROM `+tableWorktree+` WHERE path = ?`, path)
	err := row.Scan(&w.Path, &w.LeaseRepoID, &w.LeaseScopeGlob, &w.Repo, &w.Branch)
	if errors.Is(err, sql.ErrNoRows) {
		return Worktree{}, false, nil
	}
	if err != nil {
		return Worktree{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get worktree")
	}
	return w, true, nil
}
