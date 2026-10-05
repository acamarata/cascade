// Purpose: the dispatcher's use of the jobs outbox: derived keys that bind
// attempt, kind and acceptance, one ci_dispatch intent per required kind
// recorded BEFORE its sub-job runs, an idempotent re-ensure, and the row
// confirmed only after the run.
//
// SPORT: internal.ci.outbox/TESTED (P1-CI-01).
package ci

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestOutboxTransactionRollsBackFailedEffect(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	e := dispatchEntry{Ref: r.ref, Snapshot: CandidateSnapshot{AttemptID: "rollback"}, Kinds: []RequirementKind{RequirementUnit}}
	if err := r.d.ensureIntents(ctx, e); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("effect refused")
	err := r.d.outboxTx(ctx, func(tx *sql.Tx) error {
		if err := jobs.MarkEffect(ctx, tx, r.d.nowMillis(), outboxKeyFor(e, RequirementUnit)); err != nil {
			return err
		}
		return cause
	})
	if err != cause {
		t.Fatalf("callback error replaced: %v", err)
	}
	pending, err := r.d.unconfirmedKeys(ctx)
	if err != nil || len(pending) != 1 || pending[outboxKeyFor(e, RequirementUnit)].State != jobs.OutboxIntentState {
		t.Fatalf("failed effect persisted: %+v, %v", pending, err)
	}
	// A callback that closes its transaction cannot be reported as committed.
	err = r.d.outboxTx(ctx, func(tx *sql.Tx) error { return tx.Rollback() })
	assertStreamError(t, err, cascade.KindUnavailable, "commit outbox transaction")
	if !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("commit failure lost sql.ErrTxDone: %v", err)
	}
}

func TestOutboxStorageFailureRefusesIntent(t *testing.T) {
	r := newRig(t)
	if err := r.jobsDB.Close(); err != nil {
		t.Fatal(err)
	}
	err := r.d.ensureIntents(context.Background(), dispatchEntry{Kinds: []RequirementKind{RequirementUnit}})
	assertStreamError(t, err, cascade.KindUnavailable, "begin outbox transaction")
	if _, err := r.d.unconfirmedKeys(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("closed outbox query = %v, want unavailable", err)
	}
}

func TestOutboxKeysBindAttemptKindAndAcceptance(t *testing.T) {
	e := dispatchEntry{Ref: JobRef{JobID: "j", AttemptGeneration: 3}, Snapshot: CandidateSnapshot{AttemptID: "a1"}}
	base := outboxKeyFor(e, RequirementLint)
	if base != outboxKeyFor(e, RequirementLint) {
		t.Fatal("the key must be stable")
	}
	other := e
	other.Snapshot.AttemptID = "a2"
	acc := e
	acc.Acceptance = true
	gen := e
	gen.Ref.AttemptGeneration = 4
	for name, key := range map[string]string{
		"kind": outboxKeyFor(e, RequirementUnit), "attempt": outboxKeyFor(other, RequirementLint),
		"acceptance": outboxKeyFor(acc, RequirementLint), "generation": outboxKeyFor(gen, RequirementLint),
	} {
		if key == base {
			t.Fatalf("the key ignores the %s", name)
		}
	}
	if !strings.HasPrefix(base, "ci_dispatch:j:3:") {
		t.Fatalf("key %q is not the jobs outbox's derived shape for site ci_dispatch", base)
	}
	if want := jobs.DeriveIdempotencyKey(jobs.OutboxSiteCIDispatch, "j", 3, payloadHashFor("a1", RequirementLint, false)); base != want {
		t.Fatalf("key = %q, want jobs.DeriveIdempotencyKey's %q", base, want)
	}
}

func TestOutboxIntentPrecedesRunAndConfirmFollowsIt(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var seen []string
	r.exec.fn = func(SubJob) (SubJobResult, error) {
		var n int
		_ = r.jobsDB.QueryRow(`SELECT COUNT(*) FROM jobs_outbox WHERE state = 'intent' AND site = 'ci_dispatch'`).Scan(&n)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, string(rune('0'+n)))
		return SubJobResult{RunID: -int64(len(seen)), RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
	}
	r.commit(map[string]string{"docs/a.md": "a"})
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	mu.Lock()
	for _, n := range seen {
		if n < "1" {
			t.Fatalf("a sub-job ran with %s intent rows recorded, want its own intent present first", n)
		}
	}
	mu.Unlock()
	if got := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE site = 'ci_dispatch' AND state = 'confirmed'`); got != 2 {
		t.Fatalf("confirmed rows = %d, want one per kind (2)", got)
	}
	if len(seen) != 2 {
		t.Fatalf("sub-jobs ran %d times (positive control), want 2", len(seen))
	}
}

func TestEnsureIntentsIsIdempotentAndKeepsState(t *testing.T) {
	r := newRig(t)
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	att, ok, err := r.d.getAttempt(context.Background(), snap.AttemptID)
	if err != nil || !ok || len(att.Entries) != 1 {
		t.Fatalf("attempt = %+v ok=%v err=%v, want one recorded dispatch", att, ok, err)
	}
	for i := 0; i < 2; i++ {
		if err := r.d.ensureIntents(context.Background(), att.Entries[0]); err != nil {
			t.Fatalf("ensureIntents: %v", err)
		}
	}
	if rows := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox`); rows != 2 {
		t.Fatalf("outbox rows = %d, want 2 (re-ensuring must not add rows)", rows)
	}
	if open := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state != 'confirmed'`); open != 0 {
		t.Fatalf("re-ensuring reopened %d confirmed rows", open)
	}
}
