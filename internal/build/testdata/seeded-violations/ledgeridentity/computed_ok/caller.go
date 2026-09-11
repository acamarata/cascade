// Package computed_ok is the false-positive probe for
// TestLedgerIdentityGate_ComputedSetIDNotFlagged: a SetID key IS present
// (so it is never "missing"), but its value depends on a function
// argument rather than being a plain string literal — mirroring
// internal/storage/plugin_migrate.go's pluginSetID(pluginID). The gate
// must not attempt (and must not fail attempting) to resolve this to a
// duplicate-checkable string; presence alone is satisfied.
package computed_ok

import "github.com/acamarata/cascade/internal/storage/migrate"

func setFor(id string) migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "computed:" + id,
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}

func setForCall(id string) migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         computedSetID(id),
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}

func computedSetID(id string) string { return "computed-call:" + id }
