// Package dup_a is a seeded fixture for TestLedgerIdentityGate_
// SeededViolationRed: it claims SetID "dup-slot", the SAME literal
// string dup_b's fixture claims — a different package, so the gate must
// flag the second occurrence as a "duplicate".
package dup_a

import "github.com/acamarata/cascade/internal/storage/migrate"

func setA() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "dup-slot",
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}
