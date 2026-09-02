// Purpose: DomainPruner.Prune's per-domain execution mechanics — dbExecer
//
//	(the round-trip-observable *sql.DB seam), pruneDomain (validate-then-
//	delete for one domain), and deleteOlderThan (the batched-DELETE loop
//	itself). Split from retention.go as a sibling file (R-14.117: Art.10.3's
//	300-line cap; mechanical relocation, no behavior change — retention.go,
//	retention_validate.go, retention_prune.go, and retention_vacuum.go
//	together are the one P1-E02-W1-S03-T2 unit).
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// dbExecer is the minimal *sql.DB method set pruneDomain/deleteOlderThan/
// validateTimestampColumnType need. *sql.DB satisfies it directly (Prune's
// public signature keeps the contract's literal `db *sql.DB` parameter
// type), and this package's own tests use it to wrap a real modernc-sqlite
// *sql.DB with round-trip COUNTING instrumentation — Art.2 is preserved
// because the interface only lets a test OBSERVE how many DELETE
// statements ran against the real engine; it never substitutes a fake one.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// pruneDomain runs one domain's prune pass and always returns a
// fully-populated PruneReport (Elapsed measured via cfg.Clock, never
// time.Now). Every registered target is validated (R-14.144 domain
// ownership, nit-4 timestamp-column type) BEFORE any DELETE is issued for
// this domain — a validation failure on any one target aborts the whole
// domain's pass with zero rows deleted, never a partial prune followed by
// a refusal.
func pruneDomain(ctx context.Context, db dbExecer, cfg RetentionConfig, meta DomainMeta) PruneReport {
	domain := meta.ID
	start := cfg.Clock.Now()
	window := cfg.DomainRetention[domain]
	if window == 0 {
		return PruneReport{Domain: domain, RowsDeleted: 0, Elapsed: cfg.Clock.Now().Sub(start)}
	}

	targets := cfg.Targets[domain]
	if len(targets) == 0 {
		err := cascade.Newf(cascade.KindInvalidInput,
			"storage: domain %s has a non-zero retention window but no registered PruneTarget", domain)
		return PruneReport{Domain: domain, RowsDeleted: 0, Elapsed: cfg.Clock.Now().Sub(start), Err: err}
	}
	for _, target := range targets {
		if err := validateTarget(ctx, db, meta, target); err != nil {
			return PruneReport{Domain: domain, RowsDeleted: 0, Elapsed: cfg.Clock.Now().Sub(start), Err: err}
		}
	}

	cutoff := cfg.Clock.Now().Add(-window).Unix()
	total := 0
	for _, target := range targets {
		n, err := deleteOlderThan(ctx, db, target, cutoff, cfg.BatchCap)
		total += n
		if err != nil {
			return PruneReport{
				Domain: domain, RowsDeleted: total, Elapsed: cfg.Clock.Now().Sub(start),
				Err: cascade.Wrapf(cascade.KindUnavailable, err, "storage: prune %s.%s", domain, target.Table),
			}
		}
	}
	return PruneReport{Domain: domain, RowsDeleted: total, Elapsed: cfg.Clock.Now().Sub(start)}
}

// deleteOlderThan COUNTs rows in target strictly older than cutoff, then
// issues exactly ceil(count/batchCap) batched DELETEs (each removing at
// most batchCap rows via a rowid subselect — plain SQLite tables have an
// implicit rowid) — never an extra empty confirming round-trip, and a
// zero count issues no DELETE at all. This split makes the round-trip
// count exactly testable (TestPruneBatchCap: 1500 rows / cap 500 = 3
// DELETEs): looping "until a round deletes fewer than batchCap" would add
// a spurious 4th round whenever count is an exact multiple of batchCap.
// batchCap is guaranteed positive here — Prune rejects a non-positive
// BatchCap (R-14.145, validateBatchCap) before pruneDomain is ever called.
func deleteOlderThan(ctx context.Context, db dbExecer, target PruneTarget, cutoff int64, batchCap int) (int, error) {
	table := quoteIdent(target.Table)
	col := quoteIdent(target.TimestampColumn)

	var count int
	countStmt := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s < ?`, table, col)
	if err := db.QueryRowContext(ctx, countStmt, cutoff).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}

	deleteStmt := fmt.Sprintf(
		`DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s < ? ORDER BY rowid LIMIT ?)`,
		table, table, col,
	)
	rounds := (count + batchCap - 1) / batchCap
	total := 0
	for i := 0; i < rounds; i++ {
		res, err := db.ExecContext(ctx, deleteStmt, cutoff, batchCap)
		if err != nil {
			return total, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += int(affected)
	}
	return total, nil
}
