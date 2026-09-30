//go:build postgres

// Purpose: provider.Tx implementation for the postgres Driver —
//
//	Get/Put/Delete/CompareAndSwap scoped to the *sql.Tx Driver.Tx opens.
//	Split out of postgres_store.go under R-14.117 (Art.10.3's 300-line
//	cap authorizes in-package splits; this file joins postgres.go's
//	authorized write set automatically per that ruling).
//
// TxRollsBackOnError (the hard part per the ticket brief): Driver.Tx
// (postgres_store.go) rolls back the *sql.Tx whenever fn returns a
// non-nil error and never calls Commit in that path, so nothing fn wrote
// through this type is ever visible to a caller reading through a
// different connection — proven by TestDriverTxRollback_FreshConnection in
// postgres_tx_test.go, which reads back through a SECOND *Driver over a
// SECOND *sql.DB, not through the same transaction or even the same pool.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).

package postgres

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// driverTx is the provider.Tx view handed to a Store.Tx closure: every
// method operates against the same *sql.Tx Driver.Tx opened for this job,
// so Put/Delete/CompareAndSwap made through it are all-or-nothing with the
// closure's return value.
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
	_, err := t.sqlTx.ExecContext(t.ctx, `INSERT INTO kv (namespace, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`, namespace, key, value)
	if err != nil {
		return wrapDBError(err, "postgres: tx put %s/%s", namespace, key)
	}
	return nil
}

// Delete removes key from namespace within the transaction.
func (t *driverTx) Delete(_ context.Context, namespace, key string) error {
	if _, err := t.sqlTx.ExecContext(t.ctx, `DELETE FROM kv WHERE namespace = $1 AND key = $2`, namespace, key); err != nil {
		return wrapDBError(err, "postgres: tx delete %s/%s", namespace, key)
	}
	return nil
}

// CompareAndSwap atomically writes newValue only if old matches byte-for-byte;
// nil old means the key must be absent. INSERT ON CONFLICT DO NOTHING guards
// creates, and a conditional UPDATE guards swaps. PostgreSQL rechecks the
// predicate after waiting for a concurrent writer. Exactly one affected row
// means success; zero rows means KindConflict and leaves the value unchanged.
// The recheck-after-wait behavior is READ COMMITTED's EvalPlanQual; under
// REPEATABLE READ or SERIALIZABLE a losing concurrent writer instead gets a
// 40001 serialization failure from the database, which wrapDBError maps to
// this same KindConflict.
func (t *driverTx) CompareAndSwap(_ context.Context, namespace, key string, old, newValue []byte) error {
	var result sql.Result
	var err error
	if old == nil {
		result, err = t.sqlTx.ExecContext(t.ctx, `INSERT INTO kv (namespace, key, value) VALUES ($1, $2, $3)
			ON CONFLICT (namespace, key) DO NOTHING`, namespace, key, newValue)
	} else {
		result, err = t.sqlTx.ExecContext(t.ctx, `UPDATE kv SET value = $3
			WHERE namespace = $1 AND key = $2 AND value = $4`, namespace, key, newValue, old)
	}
	if err != nil {
		return wrapDBError(err, "postgres: cas %s/%s", namespace, key)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapDBError(err, "postgres: cas row count %s/%s", namespace, key)
	}
	if affected != 1 {
		return cascade.Newf(cascade.KindConflict, "postgres: cas %s/%s: comparison failed", namespace, key)
	}
	return nil
}
