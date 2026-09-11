// Package missing_id is a seeded fixture for TestLedgerIdentityGate_
// SeededViolationRed: a migrate.MigrationSet{...} composite literal with
// no SetID key at all, which the gate must flag as "missing".
package missing_id

import "github.com/acamarata/cascade/internal/storage/migrate"

func badSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}
