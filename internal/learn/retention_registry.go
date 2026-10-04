package learn

// Purpose: the retention registry behind RegisterRetentionTable and
//   RegisterRetentionChild (contract outputs item P2-9): which tables
//   RetentionSweep sweeps by age, and which dependent tables it deletes
//   with their parent row. Held apart from retention.go so each file keeps
//   one job and stays under the line cap.
// Inputs: table and column names from package init() calls; a live *sql.DB
//   at sweep time for the shape check.
// Outputs: registrations, and KindInvalidInput refusals for a malformed
//   identifier, a duplicate, a table with no such column, or a table
//   registered both ways.
// Constraints: registration happens at init time, before any db is open, so
//   a table owned by another package is shape-checked at its FIRST SWEEP,
//   before any delete (a refusal then deletes nothing). Tables learn owns
//   are checked at registration time against a fixed allow list (the
//   finding table has no time column at all), so a registration that can
//   never sweep correctly is refused at once.
// SPORT: internal.learn.RegisterRetentionTable/ADDED,
//   internal.learn.RegisterRetentionChild/ADDED (P1-CAP-02).

import (
	"context"
	"database/sql"
	"regexp"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// retentionIdentifierPattern mirrors internal/storage/migrate's own
// (unexported) identifierPattern (dsl.go) exactly. Duplicated rather than
// imported: this ticket's write_scope does not include
// internal/storage/migrate, and migrate exports no identifier validator
// for an external package to call.
var retentionIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// retentionTableReg is one RegisterRetentionTable entry.
type retentionTableReg struct {
	table      string
	timeColumn string
}

// retentionChildReg is one RegisterRetentionChild entry: table's
// parentIDColumn holds a jobs_telemetry_outcomes.id.
type retentionChildReg struct {
	table          string
	parentIDColumn string
}

// retentionRegistry guards both lists; registration can run from any
// package's init() while a sweep reads them.
type retentionRegistry struct {
	mu       sync.Mutex
	tables   []retentionTableReg
	children []retentionChildReg
}

// defaultRetention is the process-wide registry the exported functions
// write and RetentionSweep reads unless a test supplies its own.
var defaultRetention = &retentionRegistry{}

// learnTimeColumns and learnParentIDColumns name, for each table learn
// owns, the only column a registration may use. jobs_telemetry_finding has
// no time column at all (an empty list refuses every name, including its
// integer id), and jobs_telemetry_outcomes is the parent, never a child.
var (
	learnTimeColumns = map[string][]string{
		tableTelemetryOutcomes: {"created_at"},
		tableTelemetryFinding:  {},
	}
	learnParentIDColumns = map[string][]string{
		tableTelemetryOutcomes: {},
		tableTelemetryFinding:  {"outcome_id"},
	}
)

// learnColumnAllowed reports, for a table learn owns, whether column is a
// permitted registration column. owned is false for any other table.
func learnColumnAllowed(allowed map[string][]string, table, column string) (owned, ok bool) {
	cols, owned := allowed[table]
	for _, c := range cols {
		if c == column {
			return owned, true
		}
	}
	return owned, false
}

// checkNames validates both identifiers and, for a learn-owned table, the
// column against its allow list. what names the registration kind.
func checkNames(what, table, column string, allowed map[string][]string) error {
	if !retentionIdentifierPattern.MatchString(table) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: invalid retention %s table identifier", what)
	}
	if !retentionIdentifierPattern.MatchString(column) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: invalid retention %s column identifier", what)
	}
	if owned, ok := learnColumnAllowed(allowed, table, column); owned && !ok {
		return cascade.Newf(cascade.KindInvalidInput, "learn: retention table %q has no %s column %q", table, what, column)
	}
	return nil
}

// registered reports whether table is already in either list. Callers hold mu.
func (reg *retentionRegistry) registered(table string) bool {
	for _, t := range reg.tables {
		if t.table == table {
			return true
		}
	}
	for _, c := range reg.children {
		if c.table == table {
			return true
		}
	}
	return false
}

// addTable registers table for age-based sweeping against timeColumn.
func (reg *retentionRegistry) addTable(table, timeColumn string) error {
	if err := checkNames("time", table, timeColumn, learnTimeColumns); err != nil {
		return err
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.registered(table) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: retention table %q is already registered", table)
	}
	reg.tables = append(reg.tables, retentionTableReg{table: table, timeColumn: timeColumn})
	return nil
}

// addChild registers table as a child keyed to jobs_telemetry_outcomes.id
// through parentIDColumn.
func (reg *retentionRegistry) addChild(table, parentIDColumn string) error {
	if err := checkNames("parent id", table, parentIDColumn, learnParentIDColumns); err != nil {
		return err
	}
	if table == tableTelemetryOutcomes {
		return cascade.Newf(cascade.KindInvalidInput, "learn: %q is the parent table and cannot register as its own child", table)
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.registered(table) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: retention table %q is already registered", table)
	}
	reg.children = append(reg.children, retentionChildReg{table: table, parentIDColumn: parentIDColumn})
	return nil
}

// snapshot copies both lists so a sweep never holds the lock across SQL.
func (reg *retentionRegistry) snapshot() ([]retentionTableReg, []retentionChildReg) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return append([]retentionTableReg(nil), reg.tables...), append([]retentionChildReg(nil), reg.children...)
}

// queryRower is the one method both *sql.DB and *sql.Tx share that this
// file needs.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// tableExists reports whether name is a table in q's database.
func tableExists(ctx context.Context, q queryRower, name string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "learn: retention sweep: check table %q exists", name)
	}
	return true, nil
}

// columnExists reports whether table declares column with an integer type
// (a unix-seconds time column and an outcome id are both integers).
func columnExists(ctx context.Context, q queryRower, table, column string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM pragma_table_info(?) WHERE name = ? AND upper(type) LIKE '%INT%'`, table, column).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "learn: retention sweep: read columns of %q", table)
	}
	return true, nil
}

// referencesParent reports whether table declares a foreign key to
// jobs_telemetry_outcomes.
func referencesParent(ctx context.Context, q queryRower, table string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM pragma_foreign_key_list(?) WHERE lower("table") = ?`,
		table, tableTelemetryOutcomes).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "learn: retention sweep: read foreign keys of %q", table)
	}
	return true, nil
}

// retentionTimeColumn returns the registered time column of table.
func retentionTimeColumn(tables []retentionTableReg, table string) (string, bool) {
	for _, t := range tables {
		if t.table == table {
			return t.timeColumn, true
		}
	}
	return "", false
}

// validateShapes refuses (KindInvalidInput) before any delete when a
// registered table that exists in db lacks its named column, when a table
// registered by age has a foreign key to jobs_telemetry_outcomes (it is a
// child and must use RegisterRetentionChild, or every sweep fails on the
// constraint), or when a child is registered but its parent table is not.
// A registered table absent from db is skipped (its owner never applied
// its migration here).
func validateShapes(ctx context.Context, db *sql.DB, tables []retentionTableReg, children []retentionChildReg) error {
	if _, ok := retentionTimeColumn(tables, tableTelemetryOutcomes); !ok && len(children) > 0 {
		return cascade.Newf(cascade.KindInvalidInput, "learn: retention children are registered but %q is not", tableTelemetryOutcomes)
	}
	type pair struct{ table, column, what string }
	var pairs []pair
	for _, t := range tables {
		pairs = append(pairs, pair{t.table, t.timeColumn, "time"})
	}
	for _, c := range children {
		pairs = append(pairs, pair{c.table, c.parentIDColumn, "parent id"})
	}
	for _, p := range pairs {
		exists, err := tableExists(ctx, db, p.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		has, err := columnExists(ctx, db, p.table, p.column)
		if err != nil {
			return err
		}
		if !has {
			return cascade.Newf(cascade.KindInvalidInput,
				"learn: retention table %q has no integer %s column %q", p.table, p.what, p.column)
		}
		if p.what != "time" || p.table == tableTelemetryOutcomes {
			continue
		}
		child, err := referencesParent(ctx, db, p.table)
		if err != nil {
			return err
		}
		if child {
			return cascade.Newf(cascade.KindInvalidInput,
				"learn: retention table %q has a foreign key to %q; register it with RegisterRetentionChild", p.table, tableTelemetryOutcomes)
		}
	}
	return nil
}
