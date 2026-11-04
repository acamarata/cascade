package learn

// Purpose: the SchemaVersion 3 tests: the set over seeded SchemaVersion 2 data,
//   the closed OutcomeKind, insert-once InsertDecisionOutcome and the
//   no-free-text shape of both child tables.
// SPORT: internal.learn.InsertDecisionOutcome/TESTED,
//   internal.learn.ParseOutcomeKind/TESTED (P1-TOP-10).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// requireSentinel fails unless err is want itself with its frozen message.
func requireSentinel(t *testing.T, err, want error, msg string) {
	t.Helper()
	if err != want || err.Error() != msg {
		t.Fatalf("err = %v, want the sentinel %q", err, msg)
	}
}

// newDecisionDB returns a migrated database holding one decision, id "dec-1".
func newDecisionDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedDB(t, tmplOutcome)
	if err := (SQLiteSchedulerDecisionWriter{}).Record(context.Background(), db, testDecision("dec-1")); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	return db
}

// TestEconomicsDecisionMigrationIdempotent: a database built at SchemaVersion 2
// with a seeded decision upgrades twice with nil, a fresh database applies
// twice with nil, and the seeded decision reads back exact with no child row.
func TestEconomicsDecisionMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	upgraded, fresh := openTestDB(t), openTestDB(t)
	cfg := migrate.ApplyConfig{DB: upgraded, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, migrationSetAt(2)); err != nil {
		t.Fatalf("apply SchemaVersion 2: %v", err)
	}
	if n := len(tableColumns(t, upgraded, tableDecisionOutcome)); n != 0 {
		t.Fatal("SchemaVersion 2 database already has the outcome table")
	}
	seeded := testDecision("dec-v2")
	if err := (SQLiteSchedulerDecisionWriter{}).Record(ctx, upgraded, seeded); err != nil {
		t.Fatalf("seed SchemaVersion 2 decision: %v", err)
	}
	if got := MigrationSet().SchemaVersion; got != 3 {
		t.Fatalf("MigrationSet().SchemaVersion = %d, want 3", got)
	}
	for name, db := range map[string]*sql.DB{"fresh": fresh, "over version 2": upgraded} {
		for pass := 1; pass <= 2; pass++ {
			if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
				t.Fatalf("%s: ApplyLearnSchema pass %d: %v", name, pass, err)
			}
		}
		var top int
		if err := db.QueryRow(`SELECT MAX(schema_version) FROM applied_migrations WHERE set_id = ?`, learnSetID).Scan(&top); err != nil || top != 3 {
			t.Errorf("%s: ledger top version = %d (err %v), want 3", name, top, err)
		}
		want := map[string]int{tableDecisionEconomics: 6, tableDecisionOutcome: 3}
		for table, n := range want {
			if got := len(tableColumns(t, db, table)); got != n {
				t.Errorf("%s: %s has %d columns, want %d", name, table, got, n)
			}
			if got := countRows(t, db, table); got != 0 {
				t.Errorf("%s: %s has %d rows after migration, want 0", name, table, got)
			}
		}
	}
	if got := readDecisions(t, upgraded); len(got) != 1 || got[0] != seeded {
		t.Fatalf("decision after upgrade = %+v, want [%+v]", got, seeded)
	}
	var kind string
	err := upgraded.QueryRow(`SELECT outcome_kind FROM `+tableDecisionOutcome+` WHERE decision_id = ?`, seeded.ID).Scan(&kind)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("outcome read of a version-2 decision = %q, %v; want sql.ErrNoRows", kind, err)
	}
}

// TestOutcomeKindClosedSet: exactly the closed kinds parse; anything else is
// refused by ParseOutcomeKind and by InsertDecisionOutcome with nothing stored.
func TestOutcomeKindClosedSet(t *testing.T) {
	const msg = "invalid-input: learn: outcome_kind is not one of the closed enum values"
	for _, k := range []OutcomeKind{OutcomeKindAccepted, OutcomeKindRejected, OutcomeKindUnknown} {
		if got, err := ParseOutcomeKind(string(k)); err != nil || got != k {
			t.Errorf("ParseOutcomeKind(%q) = %q, %v", k, got, err)
		}
	}
	db, ctx := newDecisionDB(t), context.Background()
	for _, raw := range []string{"", "ACCEPTED", "accepted ", "success", "free text"} {
		got, err := ParseOutcomeKind(raw)
		if got != "" {
			t.Errorf("ParseOutcomeKind(%q) kind = %q, want empty", raw, got)
		}
		requireSentinel(t, err, ErrUnknownOutcomeKind, msg)
		n, err := InsertDecisionOutcome(ctx, db, newTestClock(), "dec-1", OutcomeKind(raw))
		requireSentinel(t, err, ErrUnknownOutcomeKind, msg)
		if n != 0 {
			t.Errorf("insert of %q changed %d rows", raw, n)
		}
	}
	if !cascade.HasKind(ErrUnknownOutcomeKind, cascade.KindInvalidInput) {
		t.Error("ErrUnknownOutcomeKind has the wrong kind")
	}
	if got := countRows(t, db, tableDecisionOutcome); got != 0 {
		t.Fatalf("outcome rows after refusals = %d, want 0", got)
	}
}

// TestDecisionOutcomeInsertOnce: the first insert changes 1 row, a repeat 0, a
// conflicting kind refuses, an absent decision refuses, and the first row
// (kind and time) is what stays stored.
func TestDecisionOutcomeInsertOnce(t *testing.T) {
	db, ctx, clock := newDecisionDB(t), context.Background(), newTestClock()
	later := fakeClock{t: clock.t.Add(time.Hour)}
	if n, err := InsertDecisionOutcome(ctx, db, clock, "dec-1", OutcomeKindAccepted); err != nil || n != 1 {
		t.Fatalf("first insert = %d, %v; want 1, nil", n, err)
	}
	if n, err := InsertDecisionOutcome(ctx, db, later, "dec-1", OutcomeKindAccepted); err != nil || n != 0 {
		t.Fatalf("repeat insert = %d, %v; want 0, nil", n, err)
	}
	n, err := InsertDecisionOutcome(ctx, db, later, "dec-1", OutcomeKindRejected)
	requireSentinel(t, err, ErrOutcomeKindConflict,
		"conflict: learn: decision outcome already recorded with a different outcome_kind")
	if n != 0 {
		t.Fatalf("conflicting insert changed %d rows", n)
	}
	n, err = InsertDecisionOutcome(ctx, db, clock, "dec-absent", OutcomeKindAccepted)
	requireSentinel(t, err, ErrDecisionNotRecorded,
		"not-found: learn: decision outcome names no recorded scheduler decision")
	if n != 0 {
		t.Fatalf("absent-decision insert changed %d rows", n)
	}
	var kind string
	var at int64
	row := db.QueryRow(`SELECT outcome_kind, recorded_at FROM ` + tableDecisionOutcome)
	if err := row.Scan(&kind, &at); err != nil {
		t.Fatal(err)
	}
	if kind != string(OutcomeKindAccepted) || at != clock.t.Unix() {
		t.Fatalf("stored = (%q, %d), want the first insert's (%q, %d)", kind, at, OutcomeKindAccepted, clock.t.Unix())
	}
	if got := countRows(t, db, tableDecisionOutcome); got != 1 {
		t.Fatalf("outcome rows = %d, want 1", got)
	}
}

// TestDecisionOutcomeInsertInTx: the insert joins a caller's transaction.
func TestDecisionOutcomeInsertInTx(t *testing.T) {
	db, ctx := newDecisionDB(t), context.Background()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := InsertDecisionOutcome(ctx, tx, newTestClock(), "dec-1", OutcomeKindRejected); err != nil || n != 1 {
		t.Fatalf("insert in tx = %d, %v", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, tableDecisionOutcome); got != 0 {
		t.Fatalf("outcome rows after rollback = %d, want 0", got)
	}
}

// TestDecisionOutcomeRefusesBadArguments: every refusal runs before any SQL.
func TestDecisionOutcomeRefusesBadArguments(t *testing.T) {
	ctx, clock := context.Background(), newTestClock()
	store := &faultyOutcomeStore{}
	cases := map[string]func() (int64, error){
		"nil store": func() (int64, error) {
			return InsertDecisionOutcome(ctx, nil, clock, "dec-1", OutcomeKindAccepted)
		},
		"nil clock": func() (int64, error) {
			return InsertDecisionOutcome(ctx, store, nil, "dec-1", OutcomeKindAccepted)
		},
		"empty id": func() (int64, error) {
			return InsertDecisionOutcome(ctx, store, clock, "", OutcomeKindAccepted)
		},
		"oversize id": func() (int64, error) {
			return InsertDecisionOutcome(ctx, store, clock, strings.Repeat("d", 65), OutcomeKindAccepted)
		},
	}
	for name, call := range cases {
		if n, err := call(); n != 0 || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: = %d, %v; want 0, KindInvalidInput", name, n, err)
		}
	}
	if store.execs != 0 {
		t.Fatalf("refused calls reached SQL %d times", store.execs)
	}
}

// faultyOutcomeStore is an Execer whose result can fail; its reads run against
// the closed database in row, so a read fails the way a lost database does.
type faultyOutcomeStore struct {
	execErr, resultErr error
	row                *sql.DB
	execs              int
}

func (f *faultyOutcomeStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	f.execs++
	return f, f.execErr
}

func (f *faultyOutcomeStore) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return f.row.QueryRowContext(ctx, q, args...)
}

func (f *faultyOutcomeStore) LastInsertId() (int64, error) { return 0, nil }
func (f *faultyOutcomeStore) RowsAffected() (int64, error) { return 0, f.resultErr }

// TestDecisionOutcomeStoreFaultsAreUnavailable: a failed exec, result or read
// is KindUnavailable and changes nothing.
func TestDecisionOutcomeStoreFaultsAreUnavailable(t *testing.T) {
	closed := openTestDB(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("store fault")
	for name, store := range map[string]*faultyOutcomeStore{
		"exec":   {execErr: boom, row: closed},
		"result": {resultErr: boom, row: closed},
		"read":   {row: closed},
	} {
		n, err := InsertDecisionOutcome(context.Background(), store, newTestClock(), "dec-1", OutcomeKindAccepted)
		if n != 0 || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s: = %d, %v; want 0, KindUnavailable", name, n, err)
		}
	}
}

// TestSchedulerDecisionNoFreeText: neither child table has a free-text column,
// and the only TEXT columns are the decision id and the closed kind.
func TestSchedulerDecisionNoFreeText(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	for _, table := range []string{tableDecisionEconomics, tableDecisionOutcome} {
		cols := tableColumns(t, db, table)
		if len(cols) == 0 {
			t.Fatalf("%s has no columns", table)
		}
		for _, c := range cols {
			if freeTextColumn.MatchString(c) {
				t.Errorf("%s column %q matches the free-text pattern", table, c)
			}
		}
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?) WHERE upper(type) = 'TEXT'`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			if c != "decision_id" && c != "outcome_kind" {
				t.Errorf("%s has unexpected TEXT column %q", table, c)
			}
		}
	}
}
