package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the per-row apply half of Import's write transaction —
//
//	parsing one row line, deciding insert/update/skip/refuse per
//	ConflictStrategy, and issuing the actual INSERT/UPDATE. Split from
//	export_import.go under R-14.117 (Art.10.3's 300-line cap): the
//	stream-level control flow (header/version/PreImport/tx lifecycle)
//	and the per-row decision logic are two distinct concerns that happen
//	to compose, not one function's accidental length.
//
// SPORT: internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

// applyImportLine parses one row line and applies it per strategy,
// incrementing the matching ImportReport counter.
func applyImportLine(ctx context.Context, tx *sql.Tx, domain DomainID, line []byte, strategy ConflictStrategy, report *ImportReport) error {
	var row exportRow
	if err := json.Unmarshal(line, &row); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "storage: import parse row line")
	}
	if row.Type != recordTypeRow {
		return cascade.Newf(cascade.KindIntegrity, "storage: import refused: row line has _type %q, want %q", row.Type, recordTypeRow)
	}
	value, err := row.decodedValue()
	if err != nil {
		return err
	}
	return applyRow(ctx, tx, domain, row.Key, value, strategy, report)
}

// applyRow performs the single insert/update/skip/refuse decision for one
// (key, value) pair, per strategy. The exhaustive linter requires every
// ConflictStrategy member listed explicitly (R-14.101,
// default-signifies-exhaustive: false) — the default case below handles
// only a value outside the three declared constants (unreachable from
// Import's own exported API, since ImportOpts.ConflictStrategy has no
// other constructor, but still a caller could set an arbitrary int).
func applyRow(ctx context.Context, tx *sql.Tx, domain DomainID, key string, value []byte, strategy ConflictStrategy, report *ImportReport) error {
	exists, err := rowExists(ctx, tx, domain, key)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: import check row collision for key %q", key)
	}

	switch strategy {
	case ConflictStrategySkip:
		if exists {
			report.RowsSkipped++
			return nil
		}
		return insertRow(ctx, tx, domain, key, value, &report.RowsImported)
	case ConflictStrategyOverwrite:
		if exists {
			return updateRow(ctx, tx, domain, key, value, &report.RowsOverwritten)
		}
		return insertRow(ctx, tx, domain, key, value, &report.RowsImported)
	case ConflictStrategyError:
		if exists {
			return cascade.Newf(cascade.KindConflict, "storage: import refused: key %q already exists in domain %q", key, domain)
		}
		return insertRow(ctx, tx, domain, key, value, &report.RowsImported)
	default:
		return cascade.Newf(cascade.KindInvalidInput, "storage: import refused: unknown ConflictStrategy %d", int(strategy))
	}
}

// insertRow inserts a brand-new (domain, key) row and increments counter.
func insertRow(ctx context.Context, tx *sql.Tx, domain DomainID, key string, value []byte, counter *int) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(exportKVTable)+` (namespace, key, value) VALUES (?, ?, ?)`,
		string(domain), key, value,
	)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: import insert key %q", key)
	}
	*counter++
	return nil
}

// updateRow replaces an existing (domain, key) row's value and increments
// counter.
func updateRow(ctx context.Context, tx *sql.Tx, domain DomainID, key string, value []byte, counter *int) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE `+quoteIdent(exportKVTable)+` SET value = ? WHERE namespace = ? AND key = ?`,
		value, string(domain), key,
	)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: import overwrite key %q", key)
	}
	*counter++
	return nil
}

// String renders a ConflictStrategy for error messages/test output.
func (s ConflictStrategy) String() string {
	switch s {
	case ConflictStrategyError:
		return "Error"
	case ConflictStrategySkip:
		return "Skip"
	case ConflictStrategyOverwrite:
		return "Overwrite"
	default:
		return fmt.Sprintf("ConflictStrategy(%d)", int(s))
	}
}
