package storage

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: checkProbeWrite — StorageHealthCheck's write-round-trip probe
//
//	((d) in the ticket's check list). Split from health.go as a sibling
//	file (not a later R-14.117 remedy — done from the start) to keep
//	health.go comfortably under Art.10.3's 300-line cap alongside its
//	four other checks.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).

// checkProbeWrite inserts then immediately deletes a sentinel row in the
// reserved healthProbeTable (created by Bootstrap alongside the ten
// domain anchors, never one of them) and asserts both the insert and the
// delete actually affected exactly one row. Detects: a read-only database
// file, a corrupt/missing healthProbeTable, or any other condition that
// makes a real write fail. Does NOT detect: write LATENCY or throughput —
// only that one round-trip succeeded once.
func checkProbeWrite(ctx context.Context, db *sql.DB) CheckResult {
	exists, err := tableExists(ctx, db, healthProbeTable)
	if err != nil {
		return failResult("probe-write", cascade.Wrap(cascade.KindUnavailable, err, "storage: check health-probe table presence"))
	}
	if !exists {
		return failResult("probe-write", cascade.New(cascade.KindIntegrity, "storage: health-probe table missing — database was never bootstrapped"))
	}

	id, err := insertProbeRow(ctx, db)
	if err != nil {
		return failResult("probe-write", cascade.Wrap(classifyProbeError(err), err, "storage: probe-write insert"))
	}
	if err := deleteProbeRow(ctx, db, id); err != nil {
		// Sweep any sentinel rows left by THIS or an earlier degraded run
		// before reporting. Without this a database that is writable but
		// failing deletes accumulates rows in a reserved table on every
		// health check — an unbounded leak caused by the diagnostic itself.
		// Best effort: the delete already failed, so this may too, and the
		// original error is what the caller needs either way.
		sweepErr := sweepProbeRows(ctx, db)
		wrapped := cascade.Wrap(classifyProbeError(err), err, "storage: probe-write delete")
		if sweepErr != nil {
			wrapped = cascade.Wrap(classifyProbeError(err), err,
				"storage: probe-write delete (and sentinel sweep also failed)")
		}
		return failResult("probe-write", wrapped)
	}
	return CheckResult{OK: true, Detail: "insert+delete round-trip succeeded"}
}

// insertProbeRow inserts one sentinel row into healthProbeTable and
// returns its rowid, asserting exactly one row was affected.
func insertProbeRow(ctx context.Context, db *sql.DB) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO `+quoteIdent(healthProbeTable)+` DEFAULT VALUES`)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, cascade.Newf(cascade.KindIntegrity, "storage: probe-write insert affected %d rows, want 1", n)
	}
	return res.LastInsertId()
}

// deleteProbeRow deletes the row inserted by insertProbeRow, asserting
// exactly one row was affected.
func deleteProbeRow(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM `+quoteIdent(healthProbeTable)+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return cascade.Newf(cascade.KindIntegrity, "storage: probe-write delete affected %d rows, want 1", n)
	}
	return nil
}

// sweepProbeRows removes every sentinel row from the health-probe table.
// It runs only after a failed delete, to stop a database that accepts
// inserts but not deletes from accumulating rows in a reserved table once
// per health check.
func sweepProbeRows(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DELETE FROM `+quoteIdent(healthProbeTable))
	return err
}
