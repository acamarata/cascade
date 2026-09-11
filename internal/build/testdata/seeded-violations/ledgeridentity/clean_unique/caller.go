// Package clean_unique is the false-positive probe for
// TestLedgerIdentityGate_CleanNotFlagged: a properly-identified,
// tree-uniquely-named SetID must produce zero violations.
package clean_unique

import "github.com/acamarata/cascade/internal/storage/migrate"

func cleanSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "clean-unique-fixture",
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}
