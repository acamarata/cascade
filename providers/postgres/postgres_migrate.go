//go:build postgres

// Purpose: the live-migration injection seam — Migrator, Option and
//
//	WithMigrator — split out of postgres.go under R-14.117 (Art.10.3's
//	300-line cap authorizes in-package splits; this file joins
//	postgres.go's authorized write set automatically per that ruling).
//
// Why an injection seam and not a direct call: providers/** may import
// pkg/** only, never internal/** (Art.10.2), so this package cannot import
// internal/storage/migrate directly, even though that package is exactly
// what proves the B/S-02.T3 postgres dialect's ordered-apply,
// schema_version/minimum_reader_version refusal, and §D-18 pre-migration
// snapshot behaviors live against a real server (this ticket's
// integration_test.go does that proof, from a _test.go file, which is not
// bound by the providers/** import restriction the same way non-test
// production code is — see providers/sqlite/driver_test.go and
// providers/postgres/driver_test.go's existing precedent of importing
// internal/storage/storetest). The production Migrator VALUE — a thin
// closure over internal/storage/migrate.Apply with migrate.PostgresEmitter{}
// as its Dialect — is constructed by the composition root
// (internal/runtime/profile_server.go), which is free to import both this
// package and internal/storage/migrate, and passed in via WithMigrator.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).

package postgres

import (
	"context"
	"database/sql"
)

// Migrator applies pending schema migrations against db before Open
// returns the Driver. A nil Migrator (the Open default) skips migration
// entirely — correct for callers that manage their own schema, including
// this package's own conformance tests, which exercise the bare kv table
// schemaDDL already creates.
type Migrator func(ctx context.Context, db *sql.DB) error

// Option configures Open.
type Option func(*openConfig)

type openConfig struct {
	migrator Migrator
}

// WithMigrator injects the schema-migration step Open runs, against the
// connection pool, immediately after the base kv schema is created and
// before Open returns the Driver.
func WithMigrator(m Migrator) Option {
	return func(c *openConfig) { c.migrator = m }
}
