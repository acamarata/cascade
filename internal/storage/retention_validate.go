// Purpose: registration-time validation for one Prune call — R-14.144's
//
//	domain-ownership check (a PruneTarget's table must belong to the
//	domain it is registered under; a mismatch is a configuration error
//	refused BEFORE any DELETE is issued, never a report field nobody
//	reads) and R-14.145's non-positive-BatchCap rejection (a negative
//	BatchCap makes deleteOlderThan's round count negative, so its loop
//	never runs — zero deletes, nil error, rows silently retained; that is
//	exactly the "quietly deletes nothing" failure this ticket's own doc
//	comment already treats as unacceptable for a missing target). Also
//	closes the sibling gap the CR's live probe found while proving
//	R-14.144/R-14.145 (nit 4, NOT a ruling — a design choice this ticket
//	makes and documents): TimestampColumn's declared SQL type is checked
//	for INTEGER affinity, because a TEXT column holding ISO8601 text
//	compares false against every integer cutoff forever, the same
//	"Prune runs and reports success while deleting nothing" failure mode
//	R-14.145 targets, just reached through a schema mismatch instead of a
//	negative config value.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// validateBatchCap rejects a non-positive BatchCap as a configuration
// error (R-14.145), called once per Prune call right after Normalize.
// Normalize already replaces a zero BatchCap with defaultPruneBatchCap
// before this runs, so in practice only a negative value reaches here —
// the check is written against <= 0 rather than < 0 so it stays correct
// even if Normalize's fill-in rule ever changes.
func validateBatchCap(batchCap int) error {
	if batchCap <= 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: BatchCap must be positive, got %d", batchCap)
	}
	return nil
}

// domainOwnsTable reports whether table belongs to the domain meta
// describes: either that domain's own anchor table (domainRootTable,
// domains.go) or any table prefixed "<TablePrefix>_" — the naming
// convention every later ticket's real per-domain table follows. A table
// matching neither prefix is not this domain's to touch.
func domainOwnsTable(meta DomainMeta, table string) bool {
	return table == domainRootTable(meta.TablePrefix) || strings.HasPrefix(table, meta.TablePrefix+"_")
}

// validateTargetDomain rejects a PruneTarget whose Table does not belong
// to meta's domain (R-14.144). A CR-proved live probe showed an
// unvalidated cross-domain table name silently deletes another domain's
// rows and returns nil — this is the refusal that replaces that silent
// walk-around of the domain isolation boundary R-14.5/B-S-02.T5
// establish.
func validateTargetDomain(meta DomainMeta, target PruneTarget) error {
	if !domainOwnsTable(meta, target.Table) {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: PruneTarget %q is not owned by domain %s (TablePrefix %q) — refusing to prune a table belonging to a different domain",
			target.Table, meta.ID, meta.TablePrefix)
	}
	return nil
}

// validateTimestampColumnType rejects a PruneTarget whose TimestampColumn
// is not declared with INTEGER affinity (nit 4). SQLite's own affinity
// rule is applied verbatim, not a stricter invented one: any declared
// type containing "INT" (case-insensitive, per SQLite's type-affinity
// algorithm) gets INTEGER affinity. pragma_table_info also returns zero
// rows when Table itself does not exist, surfaced here as "column not
// found" — this check runs before any DELETE, so a nonexistent table is
// now caught here rather than surfacing later as a raw driver error from
// deleteOlderThan's first COUNT query.
func validateTimestampColumnType(ctx context.Context, db dbExecer, target PruneTarget) error {
	var declType string
	row := db.QueryRowContext(ctx,
		`SELECT type FROM pragma_table_info(?) WHERE name = ?`, target.Table, target.TimestampColumn)
	if err := row.Scan(&declType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cascade.Newf(cascade.KindInvalidInput,
				"storage: PruneTarget %s.%s: column not found (table missing or column misspelled)",
				target.Table, target.TimestampColumn)
		}
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: inspect %s.%s", target.Table, target.TimestampColumn)
	}
	if !strings.Contains(strings.ToUpper(declType), "INT") {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: PruneTarget %s.%s has declared type %q, want an INTEGER-affinity column — a non-integer column compares false against every cutoff forever, deleting nothing",
			target.Table, target.TimestampColumn, declType)
	}
	return nil
}

// validateTarget runs every registration-time check for one target,
// domain-ownership first (cheap, no DB round-trip) then column-type (one
// PRAGMA query) — called once per target, for every target, before
// pruneDomain issues any DELETE for that target's domain.
func validateTarget(ctx context.Context, db dbExecer, meta DomainMeta, target PruneTarget) error {
	if err := validateTargetDomain(meta, target); err != nil {
		return err
	}
	return validateTimestampColumnType(ctx, db, target)
}
