// Purpose: provider.Tx implementation for the sqlite driver, plus the
//   shared row-read helper both Driver.Get and Tx.Get use. Split out of
//   driver.go under R-14.117 (Art.10.3's 300-line cap authorizes in-package
//   splits; this file joins providers/sqlite/driver.go's authorized write
//   set automatically per that ruling).
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).

package sqlite

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// rowQuerier is the minimal *sql.DB / *sql.Tx surface getValue needs, so
// one implementation serves both Driver.Get (reads via the pooled read
// *sql.DB) and driverTx.Get (reads via the in-flight *sql.Tx, so a
// transaction observes its own uncommitted writes).
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// getValue reads namespace/key through q, translating sql.ErrNoRows into
// the taxonomy's KindNotFound per provider.Store.Get's documented contract.
func getValue(ctx context.Context, q rowQuerier, namespace, key string) ([]byte, error) {
	var value []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM kv WHERE namespace = ? AND key = ?`, namespace, key).Scan(&value)
	switch {
	case err == sql.ErrNoRows:
		return nil, cascade.Newf(cascade.KindNotFound, "sqlite: %s/%s", namespace, key)
	case err != nil:
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: get %s/%s", namespace, key)
	}
	return value, nil
}

// driverTx is the provider.Tx view handed to a Store.Tx closure: every
// method operates against the same *sql.Tx the write executor opened for
// this job, so Put/Delete/CompareAndSwap made through it are all-or-nothing
// with the closure's return value (executor.go rolls back on a non-nil
// error, per provider.Store.Tx's contract).
type driverTx struct {
	ctx   context.Context
	sqlTx *sql.Tx
}

var _ provider.Tx = (*driverTx)(nil)

// Get returns the value stored under key in namespace as of this
// transaction, including any not-yet-committed write this same
// transaction already made.
func (t *driverTx) Get(_ context.Context, namespace, key string) ([]byte, error) {
	return getValue(t.ctx, t.sqlTx, namespace, key)
}

// Put writes value under key in namespace within the transaction.
func (t *driverTx) Put(_ context.Context, namespace, key string, value []byte) error {
	_, err := t.sqlTx.ExecContext(t.ctx, `INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)
		ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`, namespace, key, value)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: tx put %s/%s", namespace, key)
	}
	return nil
}

// Delete removes key from namespace within the transaction.
func (t *driverTx) Delete(_ context.Context, namespace, key string) error {
	if _, err := t.sqlTx.ExecContext(t.ctx, `DELETE FROM kv WHERE namespace = ? AND key = ?`, namespace, key); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: tx delete %s/%s", namespace, key)
	}
	return nil
}

// CompareAndSwap writes newValue under key in namespace only if the
// current stored value equals old byte-for-byte (nil old means "must not
// currently exist"). The whole read-compare-write happens inside this
// transaction's *sql.Tx, which the write executor's single write
// connection has already serialized against every other write in the
// process — so there is no window for a concurrent writer to invalidate
// the comparison between the read and the write.
func (t *driverTx) CompareAndSwap(_ context.Context, namespace, key string, old, newValue []byte) error {
	current, err := getValue(t.ctx, t.sqlTx, namespace, key)
	switch {
	case err != nil && !cascade.HasKind(err, cascade.KindNotFound):
		return err
	case err != nil: // KindNotFound
		if old != nil {
			return cascade.Newf(cascade.KindConflict, "sqlite: cas %s/%s: key absent, want old=%q", namespace, key, old)
		}
	case old == nil:
		return cascade.Newf(cascade.KindConflict, "sqlite: cas %s/%s: key already exists", namespace, key)
	case string(current) != string(old):
		return cascade.Newf(cascade.KindConflict, "sqlite: cas %s/%s: stored value differs from old", namespace, key)
	}
	return t.Put(t.ctx, namespace, key, newValue)
}
