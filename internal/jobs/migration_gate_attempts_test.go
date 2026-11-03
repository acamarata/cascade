package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func newGateAttemptStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "gate.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyGateAttemptsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
			t.Fatalf("apply gate schema %d: %v", i, err)
		}
	}
	s := NewStore(db)
	if err := s.PutJob(ctx, baseJob("gate-job")); err != nil {
		t.Fatal(err)
	}
	seedGateExecution(t, s, "gate-exec-1", 1)
	return s, path
}

func seedGateExecution(t *testing.T, s *Store, id string, attempt int) {
	t.Helper()
	err := s.PutExecution(context.Background(), Execution{
		ID: id, JobID: "gate-job", Attempt: attempt, State: ExecutionRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkGateReason(t *testing.T, s *Store, executionID, want string) {
	t.Helper()
	var got string
	err := s.db.QueryRow(`SELECT reason FROM jobs_gate_attempts WHERE execution_id = ?`, executionID).Scan(&got)
	if err != nil || got != want {
		t.Fatalf("stored reason = %q, %v; want %q", got, err, want)
	}
}

func testGateSchemaGuards(t *testing.T) {
	s, _ := newGateAttemptStore(t)
	ctx := context.Background()
	if err := ApplyGateAttemptsSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("schema with nil db succeeded")
	}
	if err := ApplyGateAttemptsSchema(ctx, s.db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("schema with nil clock succeeded")
	}
}

// reapplyGateAttemptSchema verifies migration idempotence with stored denials.
func reapplyGateAttemptSchema(t *testing.T, s *Store) {
	t.Helper()
	if err := ApplyGateAttemptsSchema(context.Background(), s.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("reapply populated gate schema: %v", err)
	}
}
