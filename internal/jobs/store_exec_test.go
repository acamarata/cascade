package jobs

import (
	"context"
	"testing"
)

// TestPutExecutionOrphanRefused covers the plan-named orphan-execution
// error path.
func TestPutExecutionOrphanRefused(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	e := Execution{ID: "exec-orphan", JobID: "never-created", State: ExecutionRunning}
	if err := s.PutExecution(ctx, e); err == nil {
		t.Error("PutExecution(orphan job_id) = nil, want typed orphan-execution error")
	}
}

func TestIntegrityCheckReportsOrphans(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableExecution+` (id, job_id, attempt, state, started_at, ended_at, pgid, heartbeat_at)
		 VALUES ('exec-raw-orphan', 'job-does-not-exist', 1, 'running', 0, 0, 0, 0)`); err != nil {
		t.Fatalf("seed raw orphan row: %v", err)
	}
	orphans, err := s.IntegrityCheck(ctx)
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if len(orphans) != 1 || orphans[0] != "exec-raw-orphan" {
		t.Errorf("IntegrityCheck = %v, want [exec-raw-orphan]", orphans)
	}
}

func TestIntegrityCheckCleanIsEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-clean")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := s.PutExecution(ctx, Execution{ID: "exec-clean", JobID: j.ID, State: ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	orphans, err := s.IntegrityCheck(ctx)
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("IntegrityCheck = %v, want empty", orphans)
	}
}

func TestPutAndGetExecution(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-exec-1")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-1", JobID: j.ID, Attempt: 1, State: ExecutionRunning, StartedAt: 10}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	got, ok, err := s.GetExecution(ctx, "exec-1")
	if err != nil || !ok {
		t.Fatalf("GetExecution: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.Attempt != 1 || got.State != ExecutionRunning || got.StartedAt != 10 {
		t.Errorf("GetExecution = %+v, want attempt=1 state=running started_at=10", got)
	}
}

// TestExecutionLiveness covers the pgid and heartbeat_at round-trip plus
// the abandoned decode.
func TestExecutionLiveness(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-liveness")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-live", JobID: j.ID, Attempt: 1, State: ExecutionRunning, PGID: 4242, HeartbeatAt: 100}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	got, ok, err := s.GetExecution(ctx, e.ID)
	if err != nil || !ok {
		t.Fatalf("GetExecution: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.PGID != 4242 || got.HeartbeatAt != 100 {
		t.Errorf("GetExecution pgid/heartbeat = %d/%d, want 4242/100", got.PGID, got.HeartbeatAt)
	}
	abandoned := got
	abandoned.State = ExecutionAbandoned
	if err := s.PutExecution(ctx, abandoned); err != nil {
		t.Fatalf("PutExecution(abandoned): %v", err)
	}
	got2, _, err := s.GetExecution(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got2.State != ExecutionAbandoned {
		t.Errorf("GetExecution state = %q, want abandoned", got2.State)
	}
}

func TestPutExecutionResultRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-exec-result")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-result-1", JobID: j.ID, Attempt: 1, State: ExecutionSucceeded}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	r := ExecutionResult{ExecutionID: e.ID, OutputSummary: "ok", ArtifactRefs: []string{"art-1", "art-2"}}
	if err := s.PutExecutionResult(ctx, r); err != nil {
		t.Fatalf("PutExecutionResult: %v", err)
	}
}

func TestArtifactDataClassImmutable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-artifact")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-artifact", JobID: j.ID, Attempt: 1, State: ExecutionSucceeded}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	a := Artifact{
		ID: "art-1", JobID: j.ID, ExecutionID: e.ID, Kind: "diff", BlobKey: "blake3:abc", Path: "/tmp/x",
		ConsequenceClass: ConsequenceNormal, DataClass: DataClassInternal,
	}
	if err := s.PutArtifact(ctx, a); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	changed := a
	changed.DataClass = DataClassSecret
	if err := s.PutArtifact(ctx, changed); err == nil {
		t.Error("PutArtifact(changed data_class) = nil, want typed immutability error")
	}
	got, ok, err := s.GetArtifact(ctx, a.ID)
	if err != nil || !ok {
		t.Fatalf("GetArtifact: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.DataClass != DataClassInternal {
		t.Errorf("GetArtifact data_class = %q, want unchanged internal", got.DataClass)
	}
	// Same data_class, different consequence_class: allowed.
	changed2 := a
	changed2.ConsequenceClass = ConsequenceConsequential
	if err := s.PutArtifact(ctx, changed2); err != nil {
		t.Errorf("PutArtifact(same data_class, changed consequence) = %v, want nil", err)
	}
}

func TestPutArtifactRejectsUnknownClasses(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-artifact-bad")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-artifact-bad", JobID: j.ID, Attempt: 1, State: ExecutionRunning}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	bad := Artifact{ID: "art-bad", JobID: j.ID, ExecutionID: e.ID, ConsequenceClass: "bogus", DataClass: DataClassPublic}
	if err := s.PutArtifact(ctx, bad); err == nil {
		t.Error("PutArtifact(unknown consequence_class) = nil, want error")
	}
	bad2 := Artifact{ID: "art-bad2", JobID: j.ID, ExecutionID: e.ID, ConsequenceClass: ConsequenceTrivial, DataClass: "bogus"}
	if err := s.PutArtifact(ctx, bad2); err == nil {
		t.Error("PutArtifact(unknown data_class) = nil, want error")
	}
}

func TestPutExecutionRejectsEmptyIDs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutExecution(ctx, Execution{}); err == nil {
		t.Error("PutExecution(empty ids) = nil, want error")
	}
}

func TestPutExecutionRejectsUnknownState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-exec-bad-state")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-bad-state", JobID: j.ID, State: ExecutionState("bogus")}
	if err := s.PutExecution(ctx, e); err == nil {
		t.Error("PutExecution(unknown state) = nil, want error")
	}
}

func TestPutExecutionResultRejectsEmptyID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutExecutionResult(ctx, ExecutionResult{}); err == nil {
		t.Error("PutExecutionResult(empty execution_id) = nil, want error")
	}
}

func TestPutArtifactRejectsEmptyIDs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutArtifact(ctx, Artifact{}); err == nil {
		t.Error("PutArtifact(empty ids) = nil, want error")
	}
}

func TestGetArtifactDecodeErrorOnBadStoredClasses(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-artifact-raw-bad")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	e := Execution{ID: "exec-artifact-raw-bad", JobID: j.ID, State: ExecutionRunning}
	if err := s.PutExecution(ctx, e); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	a := Artifact{ID: "art-raw-bad", JobID: j.ID, ExecutionID: e.ID, ConsequenceClass: ConsequenceNormal, DataClass: DataClassInternal}
	if err := s.PutArtifact(ctx, a); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableArtifact+` SET consequence_class = 'bogus' WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("corrupt consequence_class: %v", err)
	}
	if _, _, err := s.GetArtifact(ctx, a.ID); err == nil {
		t.Error("GetArtifact(corrupted consequence_class) = nil error, want typed decode error")
	}
}

func TestGetExecutionNotFoundIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.GetExecution(ctx, "never-created")
	if err != nil || ok {
		t.Errorf("GetExecution(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestGetArtifactNotFoundIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.GetArtifact(ctx, "never-created")
	if err != nil || ok {
		t.Errorf("GetArtifact(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
