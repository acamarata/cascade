// Package migrate compiles a typed Go DSL (TableDef/ColumnDef/IndexDef/
// MigrationSet) into dialect-correct SQL for the two cascade.db targets:
// modernc SQLite (local profile) and standard-wire Postgres (server
// profile). It is standalone — no dependency on providers/sqlite beyond
// the *sql.DB interface seam Apply accepts — so it can be wired into any
// driver's open path without that driver importing this package directly
// (see providers/sqlite/driver.go's Migrator injection point and its
// doc comment for why).
//
// Purpose: typed schema-migration DSL + dialect emitters + an applied-
//
//	migrations ledger + a §D-18 pre-migration snapshot, per
//	P1-E02-W1-S02-T3.
//
// Inputs: MigrationSet values authored by callers (never end-user input —
//
//	table/column/index names are developer-controlled identifiers baked
//	into the binary, not runtime data). The identifier-safety rules in
//	this file exist as defense in depth regardless: an emitter must never
//	trust that a caller-supplied name is already safe.
//
// Outputs: ordered []string DDL statements (emitters), applied-migration
//
//	ledger rows, and pre-migration .db snapshots (Apply, in ledger.go).
//
// Constraints: internal/storage/migrate may import internal/** freely (it
//
//	is itself under internal/) but is deliberately kept to stdlib +
//	pkg/cascade so it stays reusable from any composition root. No CGO.
//	No bare time.Now/time.Since (forbidigo) — Apply takes an injected
//	Clock. Every identifier is validated against identifierPattern AND
//	quoted before it reaches an emitted statement — see validateIdentifier
//	and quoteIdent below, the single most important rule in this package.
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ColumnType is the DSL's closed set of portable column types. Each has an
// exact, documented mapping in both SQLiteEmitter and PostgresEmitter — see
// their per-file doc comments. There is deliberately no "raw SQL type"
// escape hatch: a type the DSL cannot map portably is a DSL change, not a
// caller workaround.
type ColumnType int

const (
	// TypeText maps to SQLite TEXT / Postgres TEXT.
	TypeText ColumnType = iota
	// TypeInteger maps to SQLite INTEGER / Postgres BIGINT (or BIGSERIAL
	// when the column is an autoincrement primary key — see
	// PostgresEmitter and ColumnDef.AutoIncrement's doc comment).
	TypeInteger
	// TypeReal maps to SQLite REAL / Postgres DOUBLE PRECISION.
	TypeReal
	// TypeBlob maps to SQLite BLOB / Postgres BYTEA.
	TypeBlob
)

// ColumnDef describes one column of a TableDef. There is intentionally no
// "default value" field: a free-text SQL default is an injection surface
// this ticket's contract does not ask for, so it is out of scope rather
// than half-safely supported.
type ColumnDef struct {
	// Name is the column identifier. Validated by validateIdentifier.
	Name string
	// Type is the column's portable type.
	Type ColumnType
	// PrimaryKey marks this column as (part of) the table's primary key.
	// A table may mark more than one column PrimaryKey to get a composite
	// key, EXCEPT when AutoIncrement is set (see AutoIncrement).
	PrimaryKey bool
	// AutoIncrement requests an auto-incrementing primary key (SQLite
	// "INTEGER PRIMARY KEY AUTOINCREMENT", Postgres "BIGSERIAL PRIMARY
	// KEY"). Only valid on a single TypeInteger column that is also the
	// table's ONLY PrimaryKey column — a composite or non-integer
	// autoincrement key is not portable and is REFUSED by both emitters
	// rather than emitted incorrectly. See emit_common.go's
	// primaryKeyPlan.
	//
	// Range (R-14.142, binding — read this before assuming both dialects
	// give you the same headroom): SQLite's AUTOINCREMENT is a rowid
	// alias, a 64-bit signed integer (max 9223372036854775807), matching
	// TypeInteger's own SQLite mapping. PostgresEmitter therefore emits
	// BIGSERIAL (bigint-backed, also 64-bit) rather than the plain SERIAL
	// (32-bit int, max 2147483647) a literal reading of this ticket's
	// contract text would suggest — SERIAL would silently cap a
	// high-volume table (an audit or event log is the obvious case) at
	// ~2.1 billion rows, failing in PRODUCTION ONLY once the server
	// profile's Postgres database outlives what a local SQLite profile
	// ever exercises in testing. The two profiles therefore agree on
	// range class: a caller relying on AutoIncrement gets the same
	// practical 64-bit ceiling on both dialects, matching the plain
	// TypeInteger->BIGINT mapping right beside this field. See
	// autoincrement_range_test.go's TestAutoincrementRangeEquivalence for
	// the enforced proof, and postgres_emitter.go's
	// postgresAutoincrementColumn for the emitted form.
	AutoIncrement bool
	// NotNull adds a NOT NULL constraint.
	NotNull bool
	// Unique adds a UNIQUE constraint.
	Unique bool
}

// ForeignKeyDef describes a table-level FOREIGN KEY constraint. OnDelete
// must be empty or one of the allowFKAction values below — it is
// validated exactly like an identifier because it is caller-supplied text
// that reaches the emitted statement (see validateFKAction).
type ForeignKeyDef struct {
	// Column is the local column the constraint applies to.
	Column string
	// RefTable is the referenced table's identifier.
	RefTable string
	// RefColumn is the referenced column's identifier.
	RefColumn string
	// OnDelete is "" (no action clause) or one of CASCADE / SET NULL /
	// RESTRICT / NO ACTION.
	OnDelete string
}

// allowedFKActions is the closed allow-list ForeignKeyDef.OnDelete is
// validated against. Free-text SQL here would be exactly as dangerous as
// an unvalidated identifier, so it gets the same allow-list treatment
// rather than being interpolated as-is.
var allowedFKActions = map[string]bool{
	"":          true,
	"CASCADE":   true,
	"SET NULL":  true,
	"RESTRICT":  true,
	"NO ACTION": true,
}

// IndexDef describes a CREATE INDEX statement.
type IndexDef struct {
	// Name is the index identifier.
	Name string
	// Table is the indexed table's identifier.
	Table string
	// Columns are the indexed columns, in order. Must be non-empty.
	Columns []string
	// Unique requests CREATE UNIQUE INDEX instead of CREATE INDEX.
	Unique bool
}

// TableDef describes one CREATE TABLE statement, including its foreign
// keys.
type TableDef struct {
	// Name is the table identifier.
	Name string
	// Columns are the table's columns, in order. Must be non-empty.
	Columns []ColumnDef
	// ForeignKeys are the table's FOREIGN KEY constraints, if any.
	ForeignKeys []ForeignKeyDef
}

// StepKind discriminates MigrationStep's two forms. The DSL is
// deliberately CREATE-only (CREATE TABLE / CREATE INDEX, both emitted
// IF NOT EXISTS) — there is no ALTER TABLE step. SQLite's historically
// limited ALTER TABLE (no DROP COLUMN before 3.35, no column-type change
// ever) and Postgres's much fuller ALTER TABLE are exactly the kind of
// divergence this ticket says must never be silently papered over; rather
// than emit an ALTER that is subtly wrong on one dialect, the DSL simply
// does not offer ALTER at all. Adding a column or changing a table's shape
// under this DSL means authoring a new MigrationStep for a new table (the
// standard forward-only SQLite migration pattern), which is portable by
// construction on both targets.
type StepKind int

const (
	// StepCreateTable requires Table to be set (Index must be nil).
	StepCreateTable StepKind = iota
	// StepCreateIndex requires Index to be set (Table must be nil).
	StepCreateIndex
)

// MigrationStep is one DDL unit within a MigrationSet. Exactly one of
// Table or Index is set, matching Kind.
type MigrationStep struct {
	Kind        StepKind
	Table       *TableDef
	Index       *IndexDef
	Description string

	// ledgerBootstrap is unexported: only ensureLedgerTable (ledger_
	// queries.go) may set it, by constructing its own MigrationStep
	// literal in-package. It is the single carve-out from
	// reservedLedgerName's refusal (R-14.143) — the package's own
	// bootstrap of the applied_migrations table is legitimate and must
	// not trip the same guard that refuses a CALLER's attempt to name a
	// table or index "applied_migrations". Being unexported, no caller
	// outside this package can ever set it to true, and json.Marshal
	// (stepChecksum) silently skips unexported fields, so adding it does
	// not change any existing step's checksum.
	ledgerBootstrap bool
}

// MigrationSet is one forward migration: every step in Steps advances the
// schema to SchemaVersion as a single unit. MinimumReaderVersion is the
// oldest on-disk schema_version this binary can still open — Apply (in
// ledger.go) refuses to proceed, returning *SchemaDowngradeError, when the
// ledger's recorded schema_version exceeds it.
type MigrationSet struct {
	// SchemaVersion is the monotonically increasing version this set
	// advances the schema to. Must be >= 1.
	SchemaVersion int
	// MinimumReaderVersion is the oldest on-disk schema_version this
	// binary understands.
	MinimumReaderVersion int
	// Steps are applied in order.
	Steps []MigrationStep
}

// identifierPattern is the single allow-list every table, column, index,
// and foreign-key reference name is checked against before it is used to
// build any SQL string. ASCII letters/digits/underscore only, must start
// with a letter or underscore, max 63 characters (Postgres's own
// identifier length limit, which SQLite comfortably accommodates too).
// This alone rules out embedded quotes, semicolons, backticks, whitespace,
// and any non-ASCII/unicode content — the classes of hostile name this
// ticket calls out by name.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// validateIdentifier is the identifier-safety gate every table/column/
// index/foreign-key name passes through before an emitter touches it.
// kind is the field's role (used only for the error message, e.g.
// "table", "column", "index"). A name that fails this check never reaches
// string concatenation — the emitters call this before building any SQL,
// not after.
func validateIdentifier(kind, name string) error {
	if !identifierPattern.MatchString(name) {
		return cascade.Newf(cascade.KindInvalidInput,
			"migrate: invalid %s identifier %q: must match ^[A-Za-z_][A-Za-z0-9_]{0,62}$", kind, name)
	}
	return nil
}

// validateFKAction checks a ForeignKeyDef.OnDelete value against the
// closed allow-list — free text here would be exactly as dangerous as an
// unvalidated identifier reaching the emitted statement.
func validateFKAction(action string) error {
	if !allowedFKActions[strings.ToUpper(action)] {
		return cascade.Newf(cascade.KindInvalidInput,
			"migrate: invalid foreign key ON DELETE action %q: must be one of CASCADE, SET NULL, RESTRICT, NO ACTION, or empty", action)
	}
	return nil
}

// quoteIdent double-quotes name for safe interpolation into a SQL
// statement, doubling any embedded quote per the ANSI SQL escaping rule
// (both SQLite and Postgres support "..." identifier quoting identically).
// Every caller of quoteIdent has already passed name through
// validateIdentifier, whose pattern admits no quote character at all —
// the doubling here is deliberate defense in depth, not the primary
// safety mechanism, so quoteIdent is safe to call even if that invariant
// is ever violated by a future change.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// stepChecksum returns a stable content hash of step, used by the ledger
// (ledger.go) to detect an already-applied step (skip) versus a changed
// step at the same position (conflict). It hashes a deterministic JSON
// encoding of the DSL struct itself — never emitted SQL text — so the
// checksum is identical across dialects and does not change if an
// emitter's formatting changes without the underlying migration changing.
func stepChecksum(step MigrationStep) string {
	// json.Marshal of a struct with no maps is deterministic: field order
	// follows struct declaration order and slices preserve their order.
	b, err := json.Marshal(step)
	if err != nil {
		// Only reachable if MigrationStep grows an unmarshalable field
		// (e.g. a chan or func) — a programming error, not a runtime
		// condition callers can hit with today's struct shape.
		panic("migrate: stepChecksum: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
