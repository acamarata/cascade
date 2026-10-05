package learn

// Purpose: SQLiteSchedulerDecisionWriter's tests: a full-field read-back
//   through a *sql.DB and through a caller's *sql.Tx (commit and rollback),
//   the closed-enum and id refusals, immutability, and the migration shape
//   (no free-text column, version-1 rows survive the upgrade).
// SPORT: internal.learn.SQLiteSchedulerDecisionWriter/TESTED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func testDecision(id string) SchedulerDecision {
	return SchedulerDecision{
		ID: id, JobID: "job-dec-1", ExecutionID: "exec-dec-1", TaskClass: "code", SelectedTier: "tier-1",
		SelectedLaneID: "lane-a", SelectedNodeID: "node-7", ScoreAtSelection: 0.8125,
		FallbackLevel: LevelLang, JumpRuleFired: true, JumpReasonCode: capacity.JumpReasonExpectedTime,
		ReserveTier0: true, DecidedAt: time.Date(2026, 9, 23, 1, 2, 3, 0, time.UTC),
	}
}

// readDecisions returns every stored decision, oldest id first.
func readDecisions(t *testing.T, q interface {
	Query(string, ...any) (*sql.Rows, error)
}) []SchedulerDecision {
	t.Helper()
	rows, err := q.Query(`SELECT id, job_id, execution_id, task_class, selected_tier, selected_lane_id, selected_node_id,
		score_at_selection, fallback_level, jump_rule_fired, jump_reason_code, reserve_tier0_flag, decided_at
		FROM ` + tableSchedulerDecision + ` ORDER BY id`)
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SchedulerDecision
	for rows.Next() {
		var d SchedulerDecision
		var jump, reserve, at int64
		var level, code string
		if err := rows.Scan(&d.ID, &d.JobID, &d.ExecutionID, &d.TaskClass, &d.SelectedTier, &d.SelectedLaneID, &d.SelectedNodeID,
			&d.ScoreAtSelection, &level, &jump, &code, &reserve, &at); err != nil {
			t.Fatalf("scan decision: %v", err)
		}
		d.FallbackLevel, d.JumpReasonCode = FallbackLevel(level), capacity.JumpReasonCode(code)
		d.JumpRuleFired, d.ReserveTier0, d.DecidedAt = jump == 1, reserve == 1, time.Unix(at, 0).UTC()
		out = append(out, d)
	}
	return out
}

// TestSchedulerDecisionRecord: Record through a *sql.DB inserts one row whose
// read-back equals every field, for both boolean polarities; an unknown enum,
// an empty or oversize id and a nil Execer are refused with nothing stored; a
// repeated id is KindConflict and leaves the first row untouched.
func TestSchedulerDecisionRecord(t *testing.T) {
	db, ctx := openMigratedDB(t, tmplOutcome), context.Background()
	w := SQLiteSchedulerDecisionWriter{}
	first := testDecision("dec-1")
	second := testDecision("dec-2")
	second.JumpRuleFired, second.ReserveTier0, second.JumpReasonCode, second.FallbackLevel = false, false, capacity.JumpReasonNone, LevelGlobal
	second.ExecutionID, second.SelectedNodeID = "", ""
	for _, d := range []SchedulerDecision{first, second} {
		if err := w.Record(ctx, db, d); err != nil {
			t.Fatalf("Record %s: %v", d.ID, err)
		}
	}
	got := readDecisions(t, db)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("read-back = %+v, want [%+v %+v]", got, first, second)
	}
	refused := map[string]func(*SchedulerDecision){
		"empty id":        func(d *SchedulerDecision) { d.ID = "" },
		"bad jump code":   func(d *SchedulerDecision) { d.JumpReasonCode = "explained: because" },
		"bad level":       func(d *SchedulerDecision) { d.FallbackLevel = "class" },
		"oversize lane":   func(d *SchedulerDecision) { d.SelectedLaneID = strings.Repeat("l", 65) },
		"empty jump code": func(d *SchedulerDecision) { d.JumpReasonCode = "" },
	}
	for name, mut := range refused {
		d := testDecision("dec-refused")
		mut(&d)
		if err := w.Record(ctx, db, d); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: Record = %v, want KindInvalidInput", name, err)
		}
	}
	if err := w.Record(ctx, nil, first); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil Execer: Record = %v, want KindInvalidInput", err)
	}
	dup := testDecision("dec-1")
	dup.SelectedTier = "tier-2"
	if err := w.Record(ctx, db, dup); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("duplicate id: Record = %v, want KindConflict", err)
	}
	if after := readDecisions(t, db); len(after) != 2 || after[0] != first {
		t.Errorf("rows after refusals = %+v, want the original two unchanged", after)
	}
}

// TestSchedulerDecisionInCallerTx: Record through a caller's *sql.Tx is
// visible inside the tx and after commit, and a rolled-back tx leaves no row.
func TestSchedulerDecisionInCallerTx(t *testing.T) {
	db, ctx := openMigratedDB(t, tmplOutcome), context.Background()
	w := SQLiteSchedulerDecisionWriter{}
	d := testDecision("dec-tx-1")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Record(ctx, tx, d); err != nil {
		t.Fatalf("Record in tx: %v", err)
	}
	if in := readDecisions(t, tx); len(in) != 1 || in[0] != d {
		t.Fatalf("rows visible inside the tx = %+v, want [%+v]", in, d)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rows := readDecisions(t, db); len(rows) != 0 {
		t.Fatalf("rows after rollback = %+v, want none", rows)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Record(ctx, tx, d); err != nil {
		t.Fatalf("Record in second tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if rows := readDecisions(t, db); len(rows) != 1 || rows[0] != d {
		t.Fatalf("rows after commit = %+v, want [%+v]", rows, d)
	}
}

// TestSchedulerDecisionStoredJobID: a job id the outcome writer would map to
// its digest id is stored the same way, so the two tables still join; a closed
// db is KindUnavailable.
func TestSchedulerDecisionStoredJobID(t *testing.T) {
	db, ctx := openMigratedDB(t, tmplOutcome), context.Background()
	d := testDecision("dec-job-1")
	d.JobID = "intent: refactor the parser"
	if err := (SQLiteSchedulerDecisionWriter{}).Record(ctx, db, d); err != nil {
		t.Fatal(err)
	}
	if got := readDecisions(t, db); len(got) != 1 || got[0].JobID != storedJobID(d.JobID) || strings.Contains(got[0].JobID, "refactor") {
		t.Fatalf("stored job_id = %+v, want the digest id %q", got, storedJobID(d.JobID))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (SQLiteSchedulerDecisionWriter{}).Record(ctx, db, testDecision("dec-closed")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Record on a closed db = %v, want KindUnavailable", err)
	}
}

var freeTextColumn = regexp.MustCompile(`(?i)(prompt|text|content|message|input|query|response|explain)`)

// tableColumns lists the column names of table via PRAGMA table_info.
func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

// TestMigrationV2OverV1: a database built at SchemaVersion 1 (the P1-CAP-02
// shape) keeps its rows, upgrades with nil, then accepts a decision whose
// read-back is exact; the new tables have the contract columns and no
// free-text column.
func TestMigrationV2OverV1(t *testing.T) {
	db, ctx := openTestDB(t), context.Background()
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, migrationSetAt(1)); err != nil {
		t.Fatalf("apply version 1: %v", err)
	}
	if len(tableColumns(t, db, tableSchedulerDecision)) != 0 {
		t.Fatal("version-1 database already has jobs_scheduler_decisions")
	}
	if _, err := db.Exec(`INSERT INTO ` + tableTelemetryOutcomes + `
		(job_id, task_class, repo_id, language, component, risk_class, lane_tier, node_id, scope_ref,
		 context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count,
		 ci_failure_count, rework_cycles, final_outcome, regression_detected, cost_tokens, quota_units, created_at)
		VALUES ('job-v1','code','r','go','c','normal','tier-1','n1','s1',0,'r',0,0,0,0,0,'accepted',0,0,0,1)`); err != nil {
		t.Fatalf("seed version-1 row: %v", err)
	}
	if err := migrate.Apply(ctx, cfg, MigrationSet()); err != nil {
		t.Fatalf("upgrade to version 2: %v", err)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tableTelemetryOutcomes + ` WHERE job_id = 'job-v1'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("version-1 outcome rows after upgrade = %d (err %v), want 1", kept, err)
	}
	want := map[string]int{tableSchedulerDecision: 13, tableCapabilityScore: 8}
	for table, n := range want {
		cols := tableColumns(t, db, table)
		if len(cols) != n {
			t.Errorf("%s has %d columns %v, want %d", table, len(cols), cols, n)
		}
		for _, c := range cols {
			if freeTextColumn.MatchString(c) {
				t.Errorf("%s column %q looks like free text", table, c)
			}
		}
	}
	d := testDecision("dec-upgraded")
	if err := (SQLiteSchedulerDecisionWriter{}).Record(ctx, db, d); err != nil {
		t.Fatal(err)
	}
	if got := readDecisions(t, db); len(got) != 1 || got[0] != d {
		t.Errorf("decision read back from the upgraded database = %+v, want [%+v]", got, d)
	}
}
