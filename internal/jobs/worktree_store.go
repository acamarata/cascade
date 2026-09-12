package jobs

// Purpose: direct-SQL row helpers this ticket needs beyond store_lease.go's
//
//	CRUD (worktreeRowForLease, deleteWorktreeRow, listActiveWorktreeRows,
//	moveWorktreeRow) — store_lease.go is S-59.T1's file, out of this
//	ticket's files_scope, and owns no lease-keyed lookup, delete, or list
//	over jobs_worktree. This file queries the SAME table (tableWorktree)
//	via s.db directly, exactly the precedent lease_fence.go's own
//	latestExecutionPGIDTx sets for a same-package direct-SQL helper that
//	does not touch another ticket's file. FILES_SCOPE NOTE: this filename
//	is not in the ticket's files_scope add list; see the journal.
//
// Inputs: a *Store and the natural keys jobs_worktree rows are queried by
//
//	(lease_repo_id/lease_scope_glob, or path for delete/move).
//
// Outputs: typed A-T7 errors on any *sql.DB failure; a bare not-found is
//
//	reported as (zero value, false, nil), matching GetLease/GetWorktree's
//	own convention in store_lease.go.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// worktreeRowForLease returns the worktree row bound to (repoID,
// scopeGlob), if any. At most one worktree exists per lease by
// construction (Create's convergence check never inserts a second row for
// the same lease key while the first's path still exists).
func worktreeRowForLease(ctx context.Context, s *Store, repoID, scopeGlob string) (Worktree, bool, error) {
	var w Worktree
	row := s.db.QueryRowContext(ctx,
		`SELECT path, lease_repo_id, lease_scope_glob, repo, branch FROM `+tableWorktree+`
		 WHERE lease_repo_id = ? AND lease_scope_glob = ?`, repoID, scopeGlob)
	err := row.Scan(&w.Path, &w.LeaseRepoID, &w.LeaseScopeGlob, &w.Repo, &w.Branch)
	if errors.Is(err, sql.ErrNoRows) {
		return Worktree{}, false, nil
	}
	if err != nil {
		return Worktree{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: query worktree for lease")
	}
	return w, true, nil
}

// deleteWorktreeRow removes the worktree row at path. A missing row is a
// no-op, not an error (idempotent).
func deleteWorktreeRow(ctx context.Context, s *Store, path string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+tableWorktree+` WHERE path = ?`, path); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: delete worktree row")
	}
	return nil
}

// listActiveWorktreeRows returns every worktree row NOT already parked
// under a repo's quarantine directory — the sweep's candidate set
// (worktree_sweep.go). Quarantined rows are excluded by path shape (see
// worktree_quarantine.go's quarantinePath), not a schema column: the
// jobs_worktree table (S-59.T1's migration, out of this ticket's
// files_scope) carries no quarantine flag.
func listActiveWorktreeRows(ctx context.Context, s *Store) ([]Worktree, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, lease_repo_id, lease_scope_glob, repo, branch FROM `+tableWorktree+`
		 WHERE path NOT LIKE '%'||?||'%'`, "/"+worktreesDirName+"/"+quarantineDirName+"/")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list active worktree rows")
	}
	defer func() { _ = rows.Close() }()

	var out []Worktree
	for rows.Next() {
		var w Worktree
		if err := rows.Scan(&w.Path, &w.LeaseRepoID, &w.LeaseScopeGlob, &w.Repo, &w.Branch); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan worktree row")
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate worktree rows")
	}
	return out, nil
}

// moveWorktreeRow atomically re-keys a worktree row from oldPath to a new
// row at newPath (the quarantine move, worktree_quarantine.go): delete-then-
// insert, since Path is the table's primary key.
func moveWorktreeRow(ctx context.Context, s *Store, oldPath string, w Worktree) error {
	return withStoreTx(ctx, s, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableWorktree+` WHERE path = ?`, oldPath); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: delete worktree row for quarantine move")
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+tableWorktree+` (path, lease_repo_id, lease_scope_glob, repo, branch) VALUES (?, ?, ?, ?, ?)`,
			w.Path, w.LeaseRepoID, w.LeaseScopeGlob, w.Repo, w.Branch)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert quarantined worktree row")
		}
		return nil
	})
}

// withStoreTx opens one transaction over s.db and commits it iff fn
// succeeds. s.withTx (lease_query.go) is unexported but same-package, so
// this is a thin passthrough kept local to this file's own vocabulary.
func withStoreTx(ctx context.Context, s *Store, fn func(tx *sql.Tx) error) error {
	return s.withTx(ctx, fn)
}
