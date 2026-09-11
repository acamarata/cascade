//go:build postgres

// Package pgvector is cascade v2's server-profile provider.VectorStore
// driver: a real jackc/pgx-backed implementation over the pgvector
// Postgres extension (02-TARGET-STRUCTURE.md §providers).
//
// Purpose: the VectorStore-family driver for the server profile, passing
//
//	the B/S-02.T1 storetest vector suite (internal/storage/storetest.
//	RunVectorStoreTests) against a REAL pgvector-enabled Postgres server —
//	the local profile's counterpart is providers/localvector.
//
// Inputs: a DSN identical in shape to providers/postgres's.
// Outputs: a *Driver satisfying provider.VectorStore, or a typed
//
//	*cascade.Error. pgvector is an EXTENSION that may not be installed on
//	the target server: Open refuses with a clear cascade.KindUnsupported
//	error naming exactly what is missing when `CREATE EXTENSION vector`
//	fails — never a silent fallback to a non-vector code path, which
//	would make similarity search quietly wrong instead of loudly absent.
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2). Pure Go, no CGO — this driver speaks pgvector's wire
//	representation as plain text (`'[v1,v2,...]'::vector`) bound as an
//	ordinary placeholder parameter, never a hand-rolled binary codec and
//	never string-concatenated from caller data (see vectorLiteral).
//	the postgres build tag applies here too — this package shares it with
//	providers/postgres because both require a live Postgres server to
//	exercise for real (Art.2); every _test.go file here MUST carry the
//	identical tag or it silently fails to compile on a machine that lacks
//	the driver (AGENT-BRIEF's build-tag warning).
//
// SPORT: providers.pgvector.VectorStore/ADDED (P1-E17-W4-S38-T4).
package pgvector

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// schemaDDL creates the namespace-scoped vectors table. The embedding
// column is an UNCONSTRAINED `vector` (no fixed dimension typmod, valid
// since pgvector 0.5.0): different namespaces are free to carry different
// embedding dimensionalities in the same physical table, matching
// provider.VectorStore's per-namespace dimensionality contract rather
// than a single database-wide one.
const schemaDDL = `CREATE TABLE IF NOT EXISTS vectors (
	namespace TEXT NOT NULL,
	id        TEXT NOT NULL,
	embedding vector NOT NULL,
	metadata  JSONB NOT NULL DEFAULT '{}'::jsonb,
	PRIMARY KEY (namespace, id)
);`

// ErrExtensionMissing is returned (wrapped) when the target server does
// not have the pgvector extension available to install. errors.Is against
// this sentinel lets a caller distinguish "no vector capability here" from
// every other KindUnsupported case.
var ErrExtensionMissing = cascade.New(cascade.KindUnsupported, "pgvector: extension \"vector\" is not available on this server")

// Driver is the real pgvector provider.VectorStore implementation.
type Driver struct {
	db *sql.DB
}

// Open dials dsn, verifies the connection, and ensures the pgvector
// extension is installed on the target database (`CREATE EXTENSION IF NOT
// EXISTS vector`). If the extension cannot be created — most commonly
// because the server binary was never built with pgvector — Open refuses
// with ErrExtensionMissing rather than returning a Driver that would
// silently fail every subsequent call. The caller MUST call Close when
// done.
func Open(ctx context.Context, dsn string) (*Driver, error) {
	if dsn == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "pgvector: dsn is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pgvector: open")
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pgvector: connect")
	}
	if err := ensureExtension(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
		_ = db.Close()
		return nil, wrapDBError(err, "pgvector: schema init")
	}
	return &Driver{db: db}, nil
}

// ensureExtension installs the pgvector extension if it is not already
// present, refusing with ErrExtensionMissing (wrapping the real cause)
// when the server cannot satisfy CREATE EXTENSION — e.g. the extension's
// control file is absent from the server's install, or the role lacks
// privilege. Never falls back to any non-vector behavior.
func ensureExtension(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return cascade.Wrap(cascade.KindUnsupported, errors.Join(ErrExtensionMissing, err),
			"pgvector: CREATE EXTENSION vector failed — the server may not have pgvector installed")
	}
	return nil
}

// Close closes the connection pool.
func (d *Driver) Close() error {
	if err := d.db.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "pgvector: close")
	}
	return nil
}

// String identifies this driver in logs/diagnostics.
func (d *Driver) String() string { return "pgvector.Driver" }

// classifyPgError mirrors providers/postgres's classifier (kept local:
// providers/** packages do not share code across driver boundaries per
// 02-TARGET-STRUCTURE's provider-directory isolation — each driver owns
// its full error-mapping surface).
func classifyPgError(err error) cascade.Kind {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return cascade.KindUnavailable
	}
	switch pgErr.Code {
	case "23505", "23514", "40001": // unique/check violation, serialization failure
		return cascade.KindConflict
	case "23503", "23502", "22P02", "22000": // FK/not-null violation, invalid text representation,
		// data exception (pgvector's own "different vector dimensions" refusal
		// surfaces as SQLSTATE 22000 — a caller-side dimensionality mismatch,
		// not a backend unavailability).
		return cascade.KindInvalidInput
	case "28P01", "42501": // invalid password, insufficient privilege
		return cascade.KindPermissionDenied
	default:
		return cascade.KindUnavailable
	}
}

func wrapDBError(err error, format string, args ...any) error {
	return cascade.Wrapf(classifyPgError(err), err, format, args...)
}

// Upsert writes each Vector into namespace via a per-row INSERT ...
// ON CONFLICT DO UPDATE, all inside one transaction so a partial batch
// failure leaves no row half-written. Embedding values and metadata are
// bound as placeholder parameters — see vectorLiteral's doc comment for
// why the embedding is never string-concatenated despite pgvector having
// no native database/sql type.
func (d *Driver) Upsert(ctx context.Context, namespace string, vectors []provider.Vector) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDBError(err, "pgvector: begin upsert")
	}
	for _, v := range vectors {
		meta, err := metadataJSON(v.Metadata)
		if err != nil {
			_ = tx.Rollback()
			return cascade.Wrapf(cascade.KindInvalidInput, err, "pgvector: encode metadata for %s/%s", namespace, v.ID)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO vectors (namespace, id, embedding, metadata)
			VALUES ($1, $2, $3::vector, $4::jsonb)
			ON CONFLICT (namespace, id) DO UPDATE SET embedding = excluded.embedding, metadata = excluded.metadata`,
			namespace, v.ID, vectorLiteral(v.Values), meta)
		if err != nil {
			_ = tx.Rollback()
			return wrapDBError(err, "pgvector: upsert %s/%s", namespace, v.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return wrapDBError(err, "pgvector: commit upsert")
	}
	return nil
}

// Query returns namespace's best matches for req ranked by descending
// cosine similarity (1 - cosine distance, pgvector's `<=>` operator),
// capped at req.TopK. Filter, when non-empty, restricts matches to rows
// whose metadata JSONB contains every given key/value pair (JSONB `@>`
// containment), applied server-side in the same query.
func (d *Driver) Query(ctx context.Context, namespace string, req provider.VectorQuery) ([]provider.VectorMatch, error) {
	filterJSON, err := metadataJSON(req.Filter)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "pgvector: encode filter")
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, 1 - (embedding <=> $2::vector) AS score, metadata
		FROM vectors WHERE namespace = $1 AND metadata @> $4::jsonb
		ORDER BY embedding <=> $2::vector LIMIT $3`,
		namespace, vectorLiteral(req.Values), req.TopK, filterJSON)
	if err != nil {
		return nil, wrapDBError(err, "pgvector: query %s", namespace)
	}
	defer func() { _ = rows.Close() }()
	var out []provider.VectorMatch
	for rows.Next() {
		var m provider.VectorMatch
		var metaRaw []byte
		if err := rows.Scan(&m.ID, &m.Score, &metaRaw); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "pgvector: scan match")
		}
		if m.Metadata, err = decodeMetadata(metaRaw); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "pgvector: decode metadata")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError(err, "pgvector: query rows %s", namespace)
	}
	return out, nil
}

// Delete removes the vectors with the given ids from namespace. Deleting
// an absent id is not an error.
func (d *Driver) Delete(ctx context.Context, namespace string, ids []string) error {
	for _, id := range ids {
		if _, err := d.db.ExecContext(ctx, `DELETE FROM vectors WHERE namespace = $1 AND id = $2`, namespace, id); err != nil {
			return wrapDBError(err, "pgvector: delete %s/%s", namespace, id)
		}
	}
	return nil
}

// Count returns the number of vectors currently stored in namespace.
func (d *Driver) Count(ctx context.Context, namespace string) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM vectors WHERE namespace = $1`, namespace).Scan(&n)
	if err != nil {
		return 0, wrapDBError(err, "pgvector: count %s", namespace)
	}
	return n, nil
}

// Namespaces returns every namespace currently holding at least one
// vector.
func (d *Driver) Namespaces(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT namespace FROM vectors ORDER BY namespace`)
	if err != nil {
		return nil, wrapDBError(err, "pgvector: namespaces")
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "pgvector: scan namespace")
		}
		out = append(out, ns)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError(err, "pgvector: namespaces rows")
	}
	return out, nil
}

var _ provider.VectorStore = (*Driver)(nil)
