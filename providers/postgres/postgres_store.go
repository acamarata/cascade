//go:build postgres

// Purpose: provider.Store Get/Put/Delete/Scan for the postgres Driver, plus
//
//	the shared row-read helper both Driver.Get and driverTx.Get use —
//	mirrors providers/sqlite/driver.go + iterator.go's split (R-14.117
//	authorizes in-package splits under the 300-line cap; this file joins
//	postgres.go's authorized write set the same way).
//
// Constraints: every namespace/key value from a caller reaches SQL ONLY as
//
//	a bound placeholder ($1, $2, ...) — never string-concatenated. This is
//	the hard SQL-injection boundary: namespaces and keys are
//	caller-controlled (per the ticket contract), so nothing derived from
//	them may ever be spliced into a query string, including the prefix
//	range bounds Scan computes.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).

package postgres

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// rowQuerier is the minimal *sql.DB / *sql.Tx surface getValue needs, so
// one implementation serves both Driver.Get (via the pool) and
// driverTx.Get (via the in-flight *sql.Tx, so a transaction observes its
// own uncommitted writes) — identical role to providers/sqlite's
// rowQuerier.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// getValue reads namespace/key through q, translating sql.ErrNoRows into
// the taxonomy's KindNotFound per provider.Store.Get's documented
// contract.
func getValue(ctx context.Context, q rowQuerier, namespace, key string) ([]byte, error) {
	var value []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM kv WHERE namespace = $1 AND key = $2`, namespace, key).Scan(&value)
	switch {
	case err == sql.ErrNoRows:
		return nil, cascade.Newf(cascade.KindNotFound, "postgres: %s/%s", namespace, key)
	case err != nil:
		return nil, wrapDBError(err, "postgres: get %s/%s", namespace, key)
	}
	return value, nil
}

// Get returns the value stored under key in namespace.
func (d *Driver) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	return getValue(ctx, d.db, namespace, key)
}

// Put writes value under key in namespace unconditionally, using
// Postgres's native upsert (ON CONFLICT ... DO UPDATE), the same construct
// providers/sqlite uses.
func (d *Driver) Put(ctx context.Context, namespace, key string, value []byte) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO kv (namespace, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`, namespace, key, value)
	if err != nil {
		return wrapDBError(err, "postgres: put %s/%s", namespace, key)
	}
	return nil
}

// Delete removes key from namespace. Deleting an absent key is not an
// error (idempotent, per provider.Store's contract).
func (d *Driver) Delete(ctx context.Context, namespace, key string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM kv WHERE namespace = $1 AND key = $2`, namespace, key); err != nil {
		return wrapDBError(err, "postgres: delete %s/%s", namespace, key)
	}
	return nil
}

// Scan returns an Iterator over every key in namespace with the given
// prefix, in key order.
func (d *Driver) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	return newScanIterator(ctx, d.db, namespace, prefix)
}

// Tx runs fn inside one atomic Postgres transaction (sql.LevelDefault —
// READ COMMITTED — is sufficient here: every conflict this driver must
// detect, CompareAndSwap's stale-value race included, is caught by the
// explicit read-compare-write inside the SAME transaction, not by
// Postgres's own isolation level).
func (d *Driver) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	sqlTx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDBError(err, "postgres: begin tx")
	}
	if err := fn(ctx, &driverTx{ctx: ctx, sqlTx: sqlTx}); err != nil {
		_ = sqlTx.Rollback() // best-effort: the original err is what the caller sees
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return wrapDBError(err, "postgres: commit tx")
	}
	return nil
}

var _ provider.Store = (*Driver)(nil)

// scanIterator walks the *sql.Rows of a prefix-bounded SELECT in key
// order, identical in shape to providers/sqlite's scanIterator.
type scanIterator struct {
	rows    *sql.Rows
	key     string
	value   []byte
	err     error
	closed  bool
	closeFn func() error
}

var _ provider.Iterator = (*scanIterator)(nil)

// newScanIterator runs the prefix-bounded query against db and returns a
// ready Iterator. lo/hi are computed by prefixRange, never derived from
// string concatenation of prefix into the query text — they are bound as
// ordinary $2/$3 parameters like every other value here.
func newScanIterator(ctx context.Context, db *sql.DB, namespace, prefix string) (*scanIterator, error) {
	lo, hi, bounded := prefixRange(prefix)
	var rows *sql.Rows
	var err error
	switch {
	case prefix == "":
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = $1 ORDER BY key`, namespace)
	case bounded:
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = $1 AND key >= $2 AND key < $3 ORDER BY key`,
			namespace, lo, hi)
	default:
		// prefix has no valid successor (e.g. all 0xFF bytes) — open-ended
		// lower bound, same fallback providers/sqlite uses.
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = $1 AND key >= $2 ORDER BY key`, namespace, lo)
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "postgres: scan %s/%s*", namespace, prefix)
	}
	return &scanIterator{rows: rows, closeFn: rows.Close}, nil
}

// Next advances the iterator, returning false at end-of-results or error.
func (it *scanIterator) Next(_ context.Context) bool {
	if it.closed || it.err != nil {
		return false
	}
	if !it.rows.Next() {
		it.err = it.rows.Err()
		return false
	}
	if err := it.rows.Scan(&it.key, &it.value); err != nil {
		it.err = cascade.Wrap(cascade.KindUnavailable, err, "postgres: scan row")
		return false
	}
	return true
}

// Key returns the current entry's key.
func (it *scanIterator) Key() string { return it.key }

// Value returns the current entry's value.
func (it *scanIterator) Value() []byte { return it.value }

// Err returns the first error encountered during iteration.
func (it *scanIterator) Err() error { return it.err }

// Close releases the underlying *sql.Rows. Idempotent.
func (it *scanIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if err := it.closeFn(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "postgres: close scan")
	}
	return nil
}

// prefixRange computes the [lo, hi) half-open key range that matches every
// key beginning with prefix — byte-identical algorithm to
// providers/sqlite's prefixRange (same contract, same edge case: prefix
// made of all 0xFF bytes has no representable successor, so ok is false
// and the caller falls back to an open-ended lower-bound scan).
func prefixRange(prefix string) (lo, hi string, ok bool) {
	if prefix == "" {
		return "", "", false
	}
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0xFF {
			out := append([]byte(nil), b[:i+1]...)
			out[i]++
			return prefix, string(out), true
		}
	}
	return prefix, "", false
}
