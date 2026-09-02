package migrate

import (
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the structural DDL-building logic shared by SQLiteEmitter and
//
//	PostgresEmitter — walking a MigrationSet's steps, validating every
//	identifier, and deciding the primary-key/autoincrement shape — kept in
//	one place (DRY) so the two dialects can never drift on anything that
//	is NOT a genuine dialect difference. Each emitter supplies only the
//	small set of things that truly differ: how a ColumnType renders, and
//	how an autoincrement primary key renders.
//
// Inputs: a MigrationSet plus a dialectHooks implementation.
// Outputs: ordered []string DDL statements, or a *cascade.Error identifying
//
//	the first invalid identifier or unportable construct encountered.
//
// Constraints: every identifier is validated (validateIdentifier) and
//
//	quoted (quoteIdent) before it is placed in a string. No caller input
//	is ever concatenated directly.
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).

// Dialect is the interface SQLiteEmitter and PostgresEmitter both satisfy.
// Ledger.Apply (ledger.go) is written against this interface, never a
// concrete emitter type, so it stays dialect-agnostic.
type Dialect interface {
	// Name is the dialect's stable lowercase identifier ("sqlite" or
	// "postgres"), used in error messages and to gate dialect-specific
	// steps (e.g. snapshot.go's WAL checkpoint only runs for "sqlite").
	Name() string
	// Emit renders set into ordered DDL statements.
	Emit(set MigrationSet) ([]string, error)
}

// dialectHooks is the small set of genuinely dialect-specific rendering
// decisions. columnType maps a ColumnType to its SQL type keyword.
// autoincrementColumn renders the full column clause (type + PRIMARY KEY +
// autoincrement keyword) for the one AutoIncrement column a table may
// have — SQLite and Postgres spell this completely differently
// ("INTEGER PRIMARY KEY AUTOINCREMENT" vs "BIGSERIAL PRIMARY KEY"), so it
// cannot be built from columnType alone.
type dialectHooks struct {
	name                string
	columnType          func(ColumnType) (string, error)
	autoincrementColumn func(colName string) (string, error)
}

// emitSet walks set.Steps in order and renders each into one DDL
// statement via hooks, returning the ordered statement list. It is called
// by both SQLiteEmitter.Emit and PostgresEmitter.Emit.
func emitSet(set MigrationSet, hooks dialectHooks) ([]string, error) {
	stmts := make([]string, 0, len(set.Steps))
	for i, step := range set.Steps {
		stmt, err := emitStep(step, hooks)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "migrate: %s: step %d", hooks.name, i)
		}
		stmts = append(stmts, stmt)
	}
	return stmts, nil
}

// emitStep renders one MigrationStep into exactly one SQL statement.
func emitStep(step MigrationStep, hooks dialectHooks) (string, error) {
	switch step.Kind {
	case StepCreateTable:
		if step.Table == nil {
			return "", cascade.New(cascade.KindInvalidInput, "migrate: StepCreateTable requires Table")
		}
		if !step.ledgerBootstrap && step.Table.Name == ledgerTableName {
			return "", newReservedLedgerNameError("table", step.Table.Name)
		}
		return emitCreateTable(*step.Table, hooks)
	case StepCreateIndex:
		if step.Index == nil {
			return "", cascade.New(cascade.KindInvalidInput, "migrate: StepCreateIndex requires Index")
		}
		if step.Index.Name == ledgerTableName {
			return "", newReservedLedgerNameError("index", step.Index.Name)
		}
		return emitCreateIndex(*step.Index)
	default:
		return "", cascade.Newf(cascade.KindInvalidInput, "migrate: unknown StepKind %d", step.Kind)
	}
}

// newReservedLedgerNameError reports R-14.143: a caller migration cannot
// name a table or index "applied_migrations" — that identifier is reserved
// for the package's own ledger table (ledger.go's ledgerDef). Without this
// refusal, a caller's CREATE TABLE applied_migrations would lose the race
// against ensureLedgerTable's own CREATE TABLE IF NOT EXISTS: whichever ran
// first would silently win, discarding the other's column shape with no
// error — the exact silent-wrong-result failure class this whole DSL
// exists to avoid (same principle as R-14.127's reserved
// "plugin.__host__.*" namespace). Uses cascade.KindInvalidInput, the same
// Kind validateIdentifier/validateFKAction already use for "this
// caller-supplied name/value is not acceptable" — a reserved-name claim is
// that same rejection class (the name is syntactically fine, but this
// particular value is not permitted), not KindUnsupported's "this
// construct shape is not portable across dialects" class, which is what
// primaryKeyPlan's refusals (composite autoincrement etc.) use instead.
func newReservedLedgerNameError(kind, name string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"migrate: %s name %q is reserved for the migration ledger and cannot be used by a caller-authored migration step",
		kind, name)
}

// emitCreateIndex renders a CREATE [UNIQUE] INDEX IF NOT EXISTS statement.
// Identical on both dialects once identifiers are quoted, so it lives here
// rather than being duplicated per emitter.
func emitCreateIndex(idx IndexDef) (string, error) {
	if err := validateIdentifier("index", idx.Name); err != nil {
		return "", err
	}
	if err := validateIdentifier("table", idx.Table); err != nil {
		return "", err
	}
	if len(idx.Columns) == 0 {
		return "", cascade.Newf(cascade.KindInvalidInput, "migrate: index %q has no columns", idx.Name)
	}
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		if err := validateIdentifier("column", c); err != nil {
			return "", err
		}
		cols[i] = quoteIdent(c)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s);",
		unique, quoteIdent(idx.Name), quoteIdent(idx.Table), strings.Join(cols, ", ")), nil
}
