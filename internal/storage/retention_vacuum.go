// Purpose: VacuumJob — the weekly WAL-checkpoint + VACUUM half of the
//
//	retention subsystem. Split from retention.go as a sibling file per
//	R-14.117 (Art.10.3's 300-line cap; mechanical relocation, no
//	behavior change — retention.go plus this file together are the one
//	P1-E02-W1-S03-T2 unit).
//
// SPORT: internal.storage.retention.VacuumJob/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// VacuumJob runs the weekly WAL-checkpoint + VACUUM pass. Clock is
// required (Run returns a KindInvalidInput error when nil) — VacuumReport
// carries an Elapsed duration and Art.7.3/R-14.136 forbid deriving it from
// a bare time.Now, so unlike DomainPruner (whose Clock arrives via
// RetentionConfig on every call), VacuumJob carries its own Clock field:
// Run's signature is fixed by this ticket's contract as
// `Run(ctx, db) (VacuumReport, error)` with no config parameter to carry
// one through.
type VacuumJob struct {
	Clock Clock
}

// Run issues `PRAGMA wal_checkpoint(FULL)` (folding every committed WAL
// frame back into the main database file — VACUUM cannot reclaim space
// still sitting in the WAL) followed by `VACUUM` against db's underlying
// file, followed by a SECOND `wal_checkpoint(FULL)`: journal_mode stays
// WAL across VACUUM, so VACUUM's own rewrite is itself journaled rather
// than landing in the main file directly — without the second checkpoint,
// FileSizeAfter would be read from a main file that has not yet absorbed
// the compaction it just paid for.
//
// # What VACUUM blocks, and for how long
//
// SQLite's VACUUM rewrites the entire database into a temporary file and
// swaps it into place; it cannot run inside a transaction and, while it
// runs, it holds a write lock on the database for the FULL duration of
// the rewrite — every other writer blocks (up to the connection's
// busy_timeout, then fails) for however long a full-file copy takes,
// which scales with the on-disk size, not with how much was reclaimed. A
// build correctly sized for a personal-scale cascade.db (low hundreds of
// MB at most) makes this a sub-second-to-low-seconds pause, not a
// concern that needs a dedicated maintenance window, but Run does not
// hide the fact that it is a stop-the-world operation for its duration —
// callers scheduling it (C/S-04.T4) should run it off-peak, exactly as
// the plan's "weekly" cadence already implies.
//
// # Crash safety mid-VACUUM
//
// VACUUM's rewrite-into-a-temp-file-then-swap design means a process
// death mid-VACUUM leaves the ORIGINAL database file untouched and the
// half-written temporary file discarded on next open — SQLite never
// swaps the temp file in until the rewrite completes successfully. This
// ticket does not need to implement any explicit crash recovery for that
// reason: the guarantee is SQLite's own, not something Run adds. What Run
// self-verifies is narrower and cheaper: FileSizeAfter is read via a
// fresh os.Stat AFTER VACUUM's ExecContext call returns without error, so
// a report is only ever produced for a VACUUM that SQLite itself
// confirmed completed — Run never fabricates a report for a run that
// errored or never finished.
//
// FileSizeBefore/FileSizeAfter come from os.Stat against the database's
// own main-file path, read via `PRAGMA database_list` (never a caller-
// supplied path parameter — Run's signature is fixed by this ticket's
// contract to exactly (ctx, db), so the file path is discovered FROM db
// itself), taken immediately before wal_checkpoint and immediately after
// VACUUM returns.
func (j VacuumJob) Run(ctx context.Context, db *sql.DB) (VacuumReport, error) {
	if j.Clock == nil {
		return VacuumReport{}, cascade.New(cascade.KindInvalidInput, "storage: VacuumJob.Run requires a non-nil Clock")
	}
	start := j.Clock.Now()

	path, err := mainDatabasePath(ctx, db)
	if err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum resolve database path")
	}

	sizeBefore, err := fileSize(path)
	if err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum stat before")
	}

	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL);`); err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum wal_checkpoint")
	}
	if _, err := db.ExecContext(ctx, `VACUUM;`); err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum VACUUM")
	}
	// VACUUM's own rewrite is itself journaled through WAL (journal_mode
	// stays WAL across VACUUM), so its changes are not yet reflected in
	// the on-disk main file until a SECOND checkpoint folds them back —
	// without this, FileSizeAfter would be read from a stale pre-VACUUM
	// main file while the compaction sits unflushed in the WAL. This is
	// the concrete reason Run checkpoints twice, not a defensive
	// duplicate of the first call.
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL);`); err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum post-VACUUM wal_checkpoint")
	}

	sizeAfter, err := fileSize(path)
	if err != nil {
		return VacuumReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: vacuum stat after")
	}

	return VacuumReport{
		FileSizeBefore: sizeBefore,
		FileSizeAfter:  sizeAfter,
		Elapsed:        j.Clock.Now().Sub(start),
	}, nil
}

// mainDatabasePath reads the "main" database's on-disk file path via
// `PRAGMA database_list`, the same real-counterpart introspection every
// other on-disk assertion in this package uses (Art.2) rather than
// threading a path the caller would otherwise have to duplicate alongside
// the *sql.DB it already opened.
func mainDatabasePath(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA database_list;`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			if file == "" {
				return "", cascade.New(cascade.KindInvalidInput, "storage: vacuum requires an on-disk database (main is in-memory)")
			}
			return file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", cascade.New(cascade.KindInvalidInput, "storage: PRAGMA database_list reported no \"main\" database")
}

// fileSize is a thin os.Stat wrapper so Run's two call sites read the same
// way.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
