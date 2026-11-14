package jobs

// Purpose: the jobs_delegation version 1 -> 2 upgrade. The migration DSL has
// no ALTER, so ApplyDelegationSchema calls upgradeDelegationTable after the
// set applies: it adds the execution_id and reservation_id columns a version
// 1 table lacks and replaces the triggers whose bodies version 2 changed.

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// delegationV2Columns are the columns version 2 adds to a version 1 table.
var delegationV2Columns = []string{"execution_id", "reservation_id"}

// delegationV2Triggers are the version 1 triggers whose bodies version 2
// changed; they are dropped and recreated with the new columns.
var delegationV2Triggers = []string{"jobs_delegation_immutable", "jobs_delegation_state_insert"}

// upgradeDelegationTable adds the missing version 2 columns, replaces the
// changed triggers and creates the rest, in one transaction: a crash leaves
// either the old table and triggers or the new ones, and a re-run is a no-op.
func upgradeDelegationTable(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: begin the delegation upgrade")
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	have, err := delegationColumns(ctx, tx)
	if err != nil {
		return err
	}
	var stmts []string
	for _, c := range delegationV2Columns {
		if !have[c] {
			stmts = append(stmts, `ALTER TABLE `+tableDelegation+` ADD COLUMN `+c+` TEXT`)
		}
	}
	for _, name := range delegationV2Triggers {
		stmts = append(stmts, `DROP TRIGGER IF EXISTS `+name)
	}
	for _, stmt := range append(stmts, delegationTriggers...) {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: upgrade the delegation table")
		}
	}
	if err = tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: commit the delegation upgrade")
	}
	return nil
}

// delegationColumns reads the delegation table's column names.
func delegationColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('`+tableDelegation+`')`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read the delegation columns")
	}
	defer func() { _ = rows.Close() }()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read the delegation columns")
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read the delegation columns")
	}
	return have, nil
}
