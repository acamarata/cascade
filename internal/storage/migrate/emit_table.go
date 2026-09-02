package migrate

import (
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: emitCreateTable renders a CREATE TABLE IF NOT EXISTS statement
//
//	shared by both dialects, including the primary-key/autoincrement
//	decision (primaryKeyPlan) that is the one place SQLite and Postgres
//	genuinely diverge in table shape.
//
// Constraints: a composite (multi-column) autoincrement primary key, or an
//
//	autoincrement column that is not TypeInteger, is not portable between
//	SQLite's rowid-aliased INTEGER PRIMARY KEY AUTOINCREMENT and Postgres's
//	SERIAL — primaryKeyPlan REFUSES both rather than emitting SQL that
//	only looks right for one dialect.
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).

// emitCreateTable renders table into one CREATE TABLE IF NOT EXISTS
// statement using hooks for the dialect-specific column-type and
// autoincrement-column rendering.
func emitCreateTable(table TableDef, hooks dialectHooks) (string, error) {
	if err := validateIdentifier("table", table.Name); err != nil {
		return "", err
	}
	if len(table.Columns) == 0 {
		return "", cascade.Newf(cascade.KindInvalidInput, "migrate: table %q has no columns", table.Name)
	}

	plan, err := primaryKeyPlan(table.Columns)
	if err != nil {
		return "", err
	}

	colClauses := make([]string, 0, len(table.Columns))
	for _, col := range table.Columns {
		clause, err := emitColumn(col, plan, hooks)
		if err != nil {
			return "", err
		}
		colClauses = append(colClauses, clause)
	}

	if plan.tableLevelPK {
		cols := make([]string, len(plan.pkColumns))
		for i, c := range plan.pkColumns {
			cols[i] = quoteIdent(c)
		}
		colClauses = append(colClauses, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(cols, ", ")))
	}

	for _, fk := range table.ForeignKeys {
		clause, err := emitForeignKey(fk)
		if err != nil {
			return "", err
		}
		colClauses = append(colClauses, clause)
	}

	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n\t%s\n);",
		quoteIdent(table.Name), strings.Join(colClauses, ",\n\t")), nil
}

// pkPlan is the result of deciding how a table's primary key is shaped:
// no PK, a single inline PK column (with or without autoincrement), or a
// table-level composite PK constraint.
type pkPlan struct {
	pkColumns        []string
	autoincrementCol string // "" if none
	tableLevelPK     bool   // true when len(pkColumns) > 1
}

// primaryKeyPlan inspects columns and decides the table's primary-key
// shape, REFUSING the two unportable combinations rather than emitting SQL
// that would only be correct on one dialect:
//   - AutoIncrement on a column that is not TypeInteger.
//   - AutoIncrement combined with a composite (multi-column) primary key.
func primaryKeyPlan(columns []ColumnDef) (pkPlan, error) {
	var plan pkPlan
	for _, col := range columns {
		if col.PrimaryKey {
			plan.pkColumns = append(plan.pkColumns, col.Name)
		}
		if col.AutoIncrement {
			if plan.autoincrementCol != "" {
				return pkPlan{}, cascade.Newf(cascade.KindUnsupported,
					"migrate: table has more than one AutoIncrement column (%q, %q): only one autoincrement primary key is portable",
					plan.autoincrementCol, col.Name)
			}
			if col.Type != TypeInteger {
				return pkPlan{}, cascade.Newf(cascade.KindUnsupported,
					"migrate: column %q is AutoIncrement but not TypeInteger: SQLite AUTOINCREMENT and Postgres SERIAL both require an integer column",
					col.Name)
			}
			if !col.PrimaryKey {
				return pkPlan{}, cascade.Newf(cascade.KindUnsupported,
					"migrate: column %q is AutoIncrement but not PrimaryKey: SQLite requires AUTOINCREMENT columns to be INTEGER PRIMARY KEY",
					col.Name)
			}
			plan.autoincrementCol = col.Name
		}
	}
	if plan.autoincrementCol != "" && len(plan.pkColumns) > 1 {
		return pkPlan{}, cascade.Newf(cascade.KindUnsupported,
			"migrate: composite primary key %v is not portable with an autoincrement column (%q): SQLite AUTOINCREMENT and Postgres SERIAL both require a single-column primary key",
			plan.pkColumns, plan.autoincrementCol)
	}
	plan.tableLevelPK = plan.autoincrementCol == "" && len(plan.pkColumns) > 1
	return plan, nil
}

// emitColumn renders one column's clause within a CREATE TABLE statement.
func emitColumn(col ColumnDef, plan pkPlan, hooks dialectHooks) (string, error) {
	if err := validateIdentifier("column", col.Name); err != nil {
		return "", err
	}

	if col.Name == plan.autoincrementCol {
		rendered, err := hooks.autoincrementColumn(col.Name)
		if err != nil {
			return "", err
		}
		return rendered, nil
	}

	sqlType, err := hooks.columnType(col.Type)
	if err != nil {
		return "", err
	}

	clause := fmt.Sprintf("%s %s", quoteIdent(col.Name), sqlType)
	if col.PrimaryKey && !plan.tableLevelPK && len(plan.pkColumns) == 1 {
		clause += " PRIMARY KEY"
	}
	if col.NotNull {
		clause += " NOT NULL"
	}
	if col.Unique {
		clause += " UNIQUE"
	}
	return clause, nil
}

// emitForeignKey renders one table-level FOREIGN KEY constraint clause.
func emitForeignKey(fk ForeignKeyDef) (string, error) {
	if err := validateIdentifier("foreign key column", fk.Column); err != nil {
		return "", err
	}
	if err := validateIdentifier("foreign key referenced table", fk.RefTable); err != nil {
		return "", err
	}
	if err := validateIdentifier("foreign key referenced column", fk.RefColumn); err != nil {
		return "", err
	}
	if err := validateFKAction(fk.OnDelete); err != nil {
		return "", err
	}
	clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
		quoteIdent(fk.Column), quoteIdent(fk.RefTable), quoteIdent(fk.RefColumn))
	if fk.OnDelete != "" {
		clause += " ON DELETE " + strings.ToUpper(fk.OnDelete)
	}
	return clause, nil
}
