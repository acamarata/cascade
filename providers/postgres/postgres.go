//go:build postgres

// Package postgres is cascade v2's server-profile provider.Store driver:
// a real jackc/pgx-backed implementation over the Postgres wire, replacing
// the P1-E02-W1-S03-T5 build-tagged seam stub (store.go, deleted by this
// ticket).
//
// Purpose: complete the Store-family driver for the server profile
//
//	(02-TARGET-STRUCTURE.md §providers), speaking the real Postgres wire
//	protocol via database/sql + the pure-Go pgx/v5/stdlib adapter — no
//	CGO (06 §2).
//
// Inputs: a libpq DSN: the postgres scheme, then a userinfo section
//
//	carrying the role and its password, then host, port, database and
//	query parameters such as sslmode. The literal form is deliberately NOT
//	written out here. Every spelling of it, placeholders included, matches
//	the repo's own url-authority-password detector at 0.90 confidence, and
//	the right answer to a credential detector firing on your comment is to
//	stop writing credential-shaped text, not to exempt the file: an
//	exemption would also hide a real DSN that landed here later.
//	The password is why the DSN is redacted everywhere it is reported;
//	see this package's redaction test.
//
//	plus optional functional Options — WithMigrator injects the schema
//	migration step, following providers/sqlite/driver.go's exact
//	injection-seam pattern (providers/** may import pkg/** only, never
//	internal/**, so the real internal/storage/migrate.Apply adapter is
//	wired in by the composition root, not by this package).
//
// Outputs: a *Driver satisfying provider.Store, or a *cascade.Error whose
//
//	Kind is chosen by postgres_errors.go's classifier — never a raw
//	fmt.Errorf, and never one that echoes the DSN's credentials (see
//	redactDSN in postgres_errors.go and TestOpen_ErrorNeverLeaksDSN).
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2). Pure Go, no CGO. Every identifier this package places in
//	a query is either a fixed literal baked into this file or bound as a
//	placeholder parameter — never string-concatenated from a caller-
//	supplied namespace or key (SQL-injection surface, see
//	postgres_store.go's doc comment).
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4 — the
//
//	B/S-03.T5 stub replaced with the real driver).
package postgres

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// schemaDDL creates the single namespace-scoped key/value table, mirroring
// providers/sqlite's kv schema (same column names, same primary key
// shape) so the two drivers store the Store family identically modulo
// dialect.
//
// COLLATE "C" on both text columns is load-bearing, not decorative: a
// fresh Postgres database's DEFAULT collation is the server's OS locale
// (commonly en_US.utf8 in the pgvector/pgvector image), which sorts and
// compares punctuation with a much lower weight than SQLite's (and Go's
// own) byte-wise comparison — under that locale, "prefix/1" >= "prefix/"
// AND "prefix/1" < "prefix0" evaluates to FALSE, because "/" collates
// almost as if absent, so Scan's prefix-range query silently returned
// zero rows for any prefix containing punctuation (found live against a
// real pgvector/pgvector:pg16 container by this ticket's docker lane,
// TestPostgresStoretestUnderDocker/Scan — TestOpen_ErrorNeverLeaksDSN and
// friends, which never string-compare key content, could not have caught
// it). "C" is Postgres's built-in byte-order collation, always available
// with no OS locale package dependency, and matches prefixRange's own
// byte-increment algorithm (postgres_store.go) exactly.
const schemaDDL = `CREATE TABLE IF NOT EXISTS kv (
	namespace TEXT COLLATE "C" NOT NULL,
	key       TEXT COLLATE "C" NOT NULL,
	value     BYTEA NOT NULL,
	PRIMARY KEY (namespace, key)
);`

// Driver is the real Postgres provider.Store implementation. One Driver
// owns one *sql.DB connection pool; unlike providers/sqlite there is no
// single-write-connection constraint — Postgres's MVCC handles concurrent
// writers natively, so every method uses the pool directly.
type Driver struct {
	db  *sql.DB
	dsn string // retained ONLY for String()'s redacted label; never logged raw
}

// Open dials dsn, verifies the connection with a Ping, creates the base kv
// schema, runs the optional injected Migrator, and returns a ready Driver.
// The caller MUST call Close when done. A malformed or unreachable dsn
// never appears in the returned error's message (see redactDSN).
func Open(ctx context.Context, dsn string, opts ...Option) (*Driver, error) {
	if dsn == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "postgres: dsn is required")
	}
	cfg := openConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, wrapConnError(err, dsn, "postgres: open")
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, wrapConnError(err, dsn, "postgres: connect")
	}

	if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
		_ = db.Close()
		return nil, wrapDBError(err, "postgres: schema init")
	}

	if cfg.migrator != nil {
		if err := cfg.migrator(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return &Driver{db: db, dsn: dsn}, nil
}

// Close closes the connection pool. Close is idempotent (database/sql's
// own Close contract).
func (d *Driver) Close() error {
	if err := d.db.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "postgres: close")
	}
	return nil
}

// String identifies this driver in logs/diagnostics WITHOUT the DSN's
// credentials — see redactDSN in postgres_errors.go.
func (d *Driver) String() string { return "postgres.Driver(" + redactDSN(d.dsn) + ")" }

var _ provider.Store = (*Driver)(nil)
