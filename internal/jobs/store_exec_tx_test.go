package jobs

// Purpose: proves PutExecutionTx applies PutExecution's checks inside the
//
//	caller's transaction, reads the job through that transaction (a read on
//	the Store handle would deadlock the single-connection test database) and
//	leaves no row when the caller rolls back.
//
// SPORT: jobs/store/ADD.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// txFixture opens a store with one job and an open transaction on its only
// connection.
func txFixture(t *testing.T, jobID string) (*Store, *sql.Tx) {
	t.Helper()
	s := newTestStore(t)
	if jobID != "" {
		if err := s.PutJob(t.Context(), baseJob(jobID)); err != nil {
			t.Fatalf("PutJob: %v", err)
		}
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return s, tx
}

func TestPutExecutionTxRollsBack(t *testing.T) {
	s, tx := txFixture(t, "job-tx-1")
	// The deadline turns "the job was read on the Store handle" into a failure
	// instead of a hang: the open tx owns the only connection.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	e := Execution{ID: "exec-tx-1", JobID: "job-tx-1", Attempt: 2, State: ExecutionRunning}
	if err := s.PutExecutionTx(ctx, tx, e); err != nil {
		t.Fatalf("PutExecutionTx: %v", err)
	}
	var attempt int
	if err := tx.QueryRowContext(ctx, `SELECT attempt FROM `+tableExecution+` WHERE id = ?`, e.ID).Scan(&attempt); err != nil || attempt != 2 {
		t.Fatalf("row inside the tx = (attempt %d, err %v), want attempt 2", attempt, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	_, ok, err := s.GetExecution(t.Context(), e.ID)
	if err != nil || ok {
		t.Fatalf("GetExecution after rollback = (ok %v, err %v), want no row", ok, err)
	}
}

func TestPutExecutionTxCommitsWithCaller(t *testing.T) {
	s, tx := txFixture(t, "job-tx-2")
	e := Execution{ID: "exec-tx-2", JobID: "job-tx-2", Attempt: 1, State: ExecutionRunning}
	if err := s.PutExecutionTx(t.Context(), tx, e); err != nil {
		t.Fatalf("PutExecutionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, ok, err := s.GetExecution(t.Context(), e.ID)
	if err != nil || !ok || got.JobID != "job-tx-2" || got.Attempt != 1 {
		t.Fatalf("GetExecution after commit = (%+v, %v, %v), want the committed row", got, ok, err)
	}
}

func TestPutExecutionTxRefusals(t *testing.T) {
	s, tx := txFixture(t, "job-tx-3")
	cases := []struct {
		name string
		tx   *sql.Tx
		e    Execution
		kind cascade.Kind
		msg  string
	}{
		{"nil tx", nil, Execution{ID: "e", JobID: "job-tx-3", State: ExecutionRunning}, cascade.KindInvalidInput, "non-nil transaction"},
		{"empty id", tx, Execution{JobID: "job-tx-3", State: ExecutionRunning}, cascade.KindInvalidInput, "execution id and job_id are required"},
		{"empty job", tx, Execution{ID: "e"}, cascade.KindInvalidInput, "execution id and job_id are required"},
		{"bad state", tx, Execution{ID: "e", JobID: "job-tx-3", State: "bogus"}, cascade.KindInvalidInput, `unknown execution state "bogus"`},
		{"orphan", tx, Execution{ID: "e", JobID: "no-such-job", State: ExecutionRunning}, cascade.KindIntegrity, `orphan execution "e": job "no-such-job" does not exist`},
	}
	for _, c := range cases {
		err := s.PutExecutionTx(t.Context(), c.tx, c.e)
		var ce *cascade.Error
		if !errors.As(err, &ce) || ce.Kind != c.kind || !strings.Contains(ce.Msg, c.msg) {
			t.Errorf("%s: err = %v, want kind %v naming %q", c.name, err, c.kind, c.msg)
		}
	}
	var n int
	if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+tableExecution).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after refusals = (%d, %v), want 0", n, err)
	}
}

func TestPutExecutionTxUpsertsLikePutExecution(t *testing.T) {
	s, tx := txFixture(t, "job-tx-4")
	e := Execution{ID: "exec-tx-4", JobID: "job-tx-4", Attempt: 1, State: ExecutionRunning}
	for _, state := range []ExecutionState{ExecutionRunning, ExecutionFailed} {
		e.State = state
		if err := s.PutExecutionTx(t.Context(), tx, e); err != nil {
			t.Fatalf("PutExecutionTx(%s): %v", state, err)
		}
	}
	var got string
	if err := tx.QueryRowContext(t.Context(), `SELECT state FROM `+tableExecution+` WHERE id = ?`, e.ID).Scan(&got); err != nil || got != string(ExecutionFailed) {
		t.Fatalf("state = (%q, %v), want failed", got, err)
	}
}

func TestJobDataClassTxReadsThroughTheTransaction(t *testing.T) {
	s, tx := txFixture(t, "job-class-1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	want := *baseJob("job-class-1").DataClass
	if got, ok, err := s.JobDataClassTx(ctx, tx, "job-class-1"); err != nil || !ok || got != want {
		t.Fatalf("JobDataClassTx = (%v, %v, %v), want (%v, true, nil)", got, ok, err, want)
	}
	if _, ok, err := s.JobDataClassTx(ctx, tx, "job-unknown"); err != nil || ok {
		t.Fatalf("JobDataClassTx(unknown) = (ok %v, err %v), want (false, nil)", ok, err)
	}
	if _, _, err := s.JobDataClassTx(ctx, nil, "job-class-1"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("JobDataClassTx(nil tx) = %v, want KindInvalidInput", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+tableJob+` SET data_class = 'bogus' WHERE id = ?`, "job-class-1"); err != nil {
		t.Fatalf("corrupt the class: %v", err)
	}
	if _, ok, err := s.JobDataClassTx(ctx, tx, "job-class-1"); err == nil || ok {
		t.Fatalf("JobDataClassTx(corrupt) = (ok %v, err %v), want an error", ok, err)
	}
}
