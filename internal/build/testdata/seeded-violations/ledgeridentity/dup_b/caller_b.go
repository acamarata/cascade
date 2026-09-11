// Package dup_b is dup_a's fixture pair — see dup_a/caller_a.go's doc
// comment. Both claim the literal SetID "dup-slot".
package dup_b

import "github.com/acamarata/cascade/internal/storage/migrate"

func setB() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "dup-slot",
		SchemaVersion: 1,
		ReaderCeiling: 1,
	}
}
