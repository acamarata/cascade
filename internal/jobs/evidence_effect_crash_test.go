package jobs

// Purpose: the crash legs of EvidenceLedger.Append's audit-effect wrapping
//
//	(P1-SEC-21): a process killed between the row's commit and its
//	confirmation (or before the row) is settled by RecoverEffects from the
//	row probe. The legs re-exec this test binary against real SQLite files,
//	the recipe internal/audit/effect_crash_test.go documents.
//
// Inputs: EVIDENCE_EFFECT_CRASH_CHILD=<mode>|<dir> makes the binary the child.
// Constraints: stored state is asserted, never events; no sleeps.
// SPORT: jobs/completion-gate/ADD (tests) (P1-SEC-21).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

const (
	evidenceCrashEnv  = "EVIDENCE_EFFECT_CRASH_CHILD"
	evidenceCrashCode = 3
	crashIdem         = "idem-crash"
)

// newEffectFixture opens a real SQLite audit log beside the jobs db in dir.
func newEffectFixture(t *testing.T, dir string) (*EvidenceLedger, *Store, *audit.EffectLog, *audit.Log) {
	t.Helper()
	store, authz := seedJobsDB(t, dir)
	astore, err := sqlite.Open(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("open audit store: %v", err)
	}
	t.Cleanup(func() { _ = astore.Close() })
	log := audit.New(astore, runtime.NewFixedClock(effectsTestNow), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	ledger, err := NewEvidenceLedger(store, runtime.NewFixedClock(effectsTestNow), el, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	return ledger, store, el, log
}

// seedJobsDB opens (creating) dir's jobs db with job-1 and exec-1 present.
// It does not touch the audit store: the SQLite driver holds an exclusive
// lock, so the crash child must be able to open it first.
func seedJobsDB(t *testing.T, dir string) (*Store, *ProducerAuthz) {
	t.Helper()
	db, err := openEvidenceDB(t, filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatalf("open jobs db: %v", err)
	}
	store := NewStore(db)
	if _, ok, _ := store.GetJob(context.Background(), "job-1"); ok {
		return store, NewProducerAuthz(store, func() bool { return true }, nil)
	}
	if err := store.PutJob(context.Background(), baseJob("job-1")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return store, alwaysAuthorized(t, store)
}

// exitBeforeConfirm is the crash: the row has committed, then the process dies.
type exitBeforeConfirm struct{ audit.EffectWriter }

func (exitBeforeConfirm) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	os.Exit(evidenceCrashCode)
	return audit.Record{}, nil
}

// runEvidenceCrashChild exits evidenceCrashCode at the crash point; any other
// exit code names the step that failed.
func runEvidenceCrashChild(spec string) {
	mode, dir, _ := strings.Cut(spec, "|")
	ctx := context.Background()
	db, err := openEvidenceDBExisting(filepath.Join(dir, "jobs.db"))
	if err != nil {
		os.Exit(4)
	}
	astore, err := sqlite.Open(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		os.Exit(5)
	}
	el, err := audit.NewEffectLog(audit.New(astore, runtime.NewFixedClock(effectsTestNow), nil))
	if err != nil {
		os.Exit(6)
	}
	store := NewStore(db)
	rec := passRecord(EvidenceBuild, crashIdem)
	if mode == "before-commit" {
		if _, err := el.BeginEffect(ctx, evidenceEffectRequest(rec, 0)); err != nil {
			os.Exit(7)
		}
		os.Exit(evidenceCrashCode)
	}
	l, err := NewEvidenceLedger(store, runtime.NewFixedClock(effectsTestNow), exitBeforeConfirm{el},
		NewProducerAuthz(store, func() bool { return true }, nil))
	if err != nil {
		os.Exit(8)
	}
	_, _ = l.Append(ctx, rec, baseAuth())
	os.Exit(9)
}

func TestEvidenceAppendCrashBeforeConfirm(t *testing.T) {
	if spec := os.Getenv(evidenceCrashEnv); spec != "" {
		runEvidenceCrashChild(spec)
		return
	}
	for _, tc := range []struct {
		mode  string
		rows  int
		phase audit.EffectPhase
	}{
		{"after-commit", 1, audit.EffectConfirmed},
		{"before-commit", 0, audit.EffectFailed},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			seedJobsDB(t, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEvidenceAppendCrashBeforeConfirm$", "-test.count=1")
			cmd.Env = append(os.Environ(), evidenceCrashEnv+"="+tc.mode+"|"+dir)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != evidenceCrashCode {
				t.Fatalf("child = %v, want exit status %d; output:\n%s", err, evidenceCrashCode, out)
			}
			assertCrashRecovered(t, dir, tc.rows, tc.phase)
		})
	}
}

func assertCrashRecovered(t *testing.T, dir string, rows int, phase audit.EffectPhase) {
	t.Helper()
	ctx := context.Background()
	ledger, store, el, log := newEffectFixture(t, dir)
	key := evidenceEffectKey("job-1", crashIdem, 0)
	if got, found, err := el.EffectState(ctx, key); err != nil || !found || got != audit.EffectIntent {
		t.Fatalf("EffectState after the crash = %q, %v, %v; want intent", got, found, err)
	}
	if n := evidenceRowCount(t, store); n != rows {
		t.Fatalf("rows after the crash = %d, want %d", n, rows)
	}
	pending, err := el.PendingEffects(ctx)
	if err != nil || len(pending) != 1 || pending[0].Key != key {
		t.Fatalf("PendingEffects = %+v, %v; want exactly %q", pending, err, key)
	}
	if err := ledger.RecoverEffects(ctx, pending); err != nil {
		t.Fatalf("RecoverEffects: %v", err)
	}
	if got, _, err := el.EffectState(ctx, key); err != nil || got != phase {
		t.Fatalf("EffectState after recovery = %q, %v; want %q", got, err, phase)
	}
	if left, err := el.PendingEffects(ctx); err != nil || len(left) != 0 {
		t.Fatalf("PendingEffects after recovery = %+v, %v; want none", left, err)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("audit Verify after recovery: %v", err)
	}
	// A retry converges on exactly one row either way.
	if _, err := ledger.Append(ctx, passRecord(EvidenceBuild, crashIdem), baseAuth()); err != nil {
		t.Fatalf("retry Append: %v", err)
	}
	if n := evidenceRowCount(t, store); n != 1 {
		t.Fatalf("rows after the retry = %d, want 1", n)
	}
}
