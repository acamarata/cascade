package migrate

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the two structured error types Apply (ledger.go) returns for
//
//	its two integrity-hazard conditions. Both wrap a *cascade.Error so
//	cascade.KindOf/cascade.HasKind/errors.Is against the taxonomy's
//	per-kind sentinels (cascade.ErrIntegrity, cascade.ErrConflict) work
//	through the standard Unwrap chain, while also exposing typed fields a
//	caller can inspect via errors.As — a message string alone cannot be
//	asserted on without parsing prose, which the ticket's
//	TestDowngradeRefusal AC explicitly rules out ("with correct fields").
//
// SPORT: internal.storage.migrate.Ledger/ADDED (P1-E02-W1-S02-T3).

// SchemaDowngradeError reports that the ledger's on-disk schema_version
// exceeds the binary's MinimumReaderVersion — the opener would otherwise
// silently open a schema newer than it understands. Wraps
// cascade.KindIntegrity (a verification step — the version check — failed).
type SchemaDowngradeError struct {
	// OnDiskVersion is the schema_version recorded in the ledger.
	OnDiskVersion int
	// MinimumReaderVersion is the binary's MigrationSet.MinimumReaderVersion.
	MinimumReaderVersion int
	inner                *cascade.Error
}

func newSchemaDowngradeError(onDisk, minReader int) *SchemaDowngradeError {
	return &SchemaDowngradeError{
		OnDiskVersion:        onDisk,
		MinimumReaderVersion: minReader,
		inner: cascade.Newf(cascade.KindIntegrity,
			"migrate: on-disk schema_version %d exceeds binary minimum_reader_version %d — refusing to open a schema newer than this binary understands",
			onDisk, minReader),
	}
}

// Error implements the error interface.
func (e *SchemaDowngradeError) Error() string { return e.inner.Error() }

// Unwrap exposes the wrapped *cascade.Error so errors.Is/errors.As and
// cascade.KindOf traverse through to KindIntegrity.
func (e *SchemaDowngradeError) Unwrap() error { return e.inner }

// MigrationConflictError reports that a previously-applied migration step
// (identified by its position within a schema_version) now has a
// different content checksum than the ledger recorded — the migration
// definition changed after it was already applied, a data-integrity
// hazard the ledger refuses to paper over by silently re-executing or
// overwriting. Wraps cascade.KindConflict.
type MigrationConflictError struct {
	// SchemaVersion is the MigrationSet.SchemaVersion being applied.
	SchemaVersion int
	// StepIndex is the zero-based position of the diverged step within
	// MigrationSet.Steps.
	StepIndex int
	// LedgerChecksum is the checksum already recorded in the ledger for
	// this position.
	LedgerChecksum string
	// NewChecksum is the checksum computed from the current step content.
	NewChecksum string
	inner       *cascade.Error
}

func newMigrationConflictError(schemaVersion, stepIndex int, ledgerChecksum, newChecksum string) *MigrationConflictError {
	return &MigrationConflictError{
		SchemaVersion:  schemaVersion,
		StepIndex:      stepIndex,
		LedgerChecksum: ledgerChecksum,
		NewChecksum:    newChecksum,
		inner: cascade.Newf(cascade.KindConflict,
			"migrate: checksum conflict at schema_version %d step %d: ledger has %s, current definition hashes to %s — the migration's content changed after it was applied",
			schemaVersion, stepIndex, shortChecksum(ledgerChecksum), shortChecksum(newChecksum)),
	}
}

// Error implements the error interface.
func (e *MigrationConflictError) Error() string { return e.inner.Error() }

// Unwrap exposes the wrapped *cascade.Error so errors.Is/errors.As and
// cascade.KindOf traverse through to KindConflict.
func (e *MigrationConflictError) Unwrap() error { return e.inner }

// shortChecksum truncates a hex checksum for error messages; the full
// value remains available via the error's LedgerChecksum/NewChecksum
// fields.
func shortChecksum(sum string) string {
	if len(sum) <= 12 {
		return sum
	}
	return sum[:12] + "..."
}
