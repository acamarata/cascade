// Purpose: provider.Iterator implementation backing Driver.Scan. Split out
//   of driver.go under R-14.117 (Art.10.3's 300-line cap authorizes
//   in-package splits; this file joins driver.go's authorized write set
//   automatically per that ruling).
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).

package sqlite

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scanIterator walks the *sql.Rows of a prefix-bounded SELECT in key order.
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
// ready Iterator. The query itself is read-only, so it is safe to issue
// against the pooled read *sql.DB without going through the write
// executor.
func newScanIterator(ctx context.Context, db *sql.DB, namespace, prefix string) (*scanIterator, error) {
	lo, hi, bounded := prefixRange(prefix)
	var rows *sql.Rows
	var err error
	switch {
	case prefix == "":
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = ? ORDER BY key`, namespace)
	case bounded:
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = ? AND key >= ? AND key < ? ORDER BY key`,
			namespace, lo, hi)
	default:
		// prefix has no valid successor (e.g. all 0xFF bytes) — fall back
		// to an open-ended lower bound; correct, just unable to use the
		// upper-bound index seek.
		rows, err = db.QueryContext(ctx, `SELECT key, value FROM kv WHERE namespace = ? AND key >= ? ORDER BY key`, namespace, lo)
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: scan %s/%s*", namespace, prefix)
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
		it.err = cascade.Wrap(cascade.KindUnavailable, err, "sqlite: scan row")
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
		return cascade.Wrap(cascade.KindUnavailable, err, "sqlite: close scan")
	}
	return nil
}

// prefixRange computes the [lo, hi) half-open key range that matches every
// key beginning with prefix: lo is prefix itself, hi is prefix with its
// final byte incremented (carrying through trailing 0xFF bytes). ok is
// false when prefix is all 0xFF bytes (or empty), which has no successor
// representable as a same-length-or-shorter string — the caller falls back
// to an open-ended lower-bound-only scan in that case.
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
