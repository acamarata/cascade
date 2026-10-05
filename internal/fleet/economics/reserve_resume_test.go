package economics

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// resumeFixture reserves one interactive row over two globs, then
// returns a fresh Reserver (a restart: no ReserveRequest in memory) and
// a log of every AcquireLeases argument set.
func resumeFixture(t *testing.T, valid func([]string) []string, acquireErr error) (*reserverFixture, *Reserver, Reservation, *[]string) {
	t.Helper()
	f := newReserverFixture(t, 100000)
	req := baseReserveRequest()
	req.RepoID, req.JobID, req.ScopeGlobs = "repo-7", "job-7", []string{"src/**", "docs/**"}
	r, err := f.reserver(t).Reserve(t.Context(), req)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	var acquired []string
	f.seams.ValidateLeases = func(_ context.Context, job string, ids []string) ([]string, error) {
		if job != "job-7" {
			t.Errorf("ValidateLeases job = %q, want the stored job-7", job)
		}
		return valid(ids), nil
	}
	f.seams.AcquireLeases = func(_ context.Context, repo, job string, globs []string) ([]string, error) {
		acquired = append(acquired, fmt.Sprint(repo, " ", job, " ", globs))
		f.rec.record("acquire_leases")
		if acquireErr != nil {
			return nil, acquireErr
		}
		return []string{"lease-new-1", "lease-new-2"}, nil
	}
	f.seams.ReleaseLeases = func(_ context.Context, ids []string) error { f.rec.record(fmt.Sprint("release", ids)); return nil }
	f.rec.logs = nil
	return f, f.reserverWithEpoch(t, "epoch-2"), r, &acquired
}

// TestReserveResumeStaleLease: after a restart the Reserver reads
// repo_id, job_id and scope_globs from the row; every stored lease valid
// keeps them; any invalid releases the valid ones and re-acquires the
// full stored scope; a failed re-acquisition rolls the row back.
func TestReserveResumeStaleLease(t *testing.T) {
	f, rv, r, _ := resumeFixture(t, func(ids []string) []string { return ids }, nil)
	kept, err := rv.ReacquireForResume(t.Context(), r.ID)
	if err != nil || fmt.Sprint(kept.LeaseIDs) != "[lease-1 lease-2]" || len(f.rec.snapshot()) != 0 {
		t.Fatalf("all valid: %+v, %v, calls %v, want the stored leases kept and no call", kept, err, f.rec.snapshot())
	}
	superset := func(ids []string) []string { return append([]string{"lease-foreign"}, ids[1:]...) }
	f, rv, r, acquired := resumeFixture(t, superset, nil)
	got, err := rv.ReacquireForResume(t.Context(), r.ID)
	if err != nil || fmt.Sprint(got.LeaseIDs) != "[lease-new-1 lease-new-2]" {
		t.Fatalf("stale lease: %+v, %v, want the re-acquired leases", got, err)
	}
	if fmt.Sprint(f.rec.snapshot()) != "[release[lease-2] acquire_leases]" || fmt.Sprint(*acquired) != "[repo-7 job-7 [src/** docs/**]]" {
		t.Fatalf("calls %v acquire args %v, want only the stored valid lease released and one call with the stored repo, job and globs", f.rec.snapshot(), *acquired)
	}
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	if fmt.Sprint(stored.LeaseIDs) != "[lease-new-1 lease-new-2]" || findStep(stored, StepLeases).Handle != "lease-new-1,lease-new-2" {
		t.Fatalf("stored = %+v, want the new leases persisted and the step handle updated", stored)
	}

	injected := errors.New("scope contended")
	f, rv, r, _ = resumeFixture(t, func([]string) []string { return nil }, injected)
	failed, err := rv.ReacquireForResume(t.Context(), r.ID)
	if !hasIdentity(err, injected) || failed.State != ReservationRolledBack {
		t.Fatalf("failed re-acquisition = %+v, %v, want rolled_back and the cause", failed, err)
	}
	if fmt.Sprint(f.rec.snapshot()) != "[acquire_leases remove_worktree]" {
		t.Fatalf("calls = %v, want one acquire then the worktree removed", f.rec.snapshot())
	}
}

// pendingRow inserts a row whose ledger stops at a pending step.
func pendingRow(t *testing.T, f *reserverFixture, id string, pending StepName) Reservation {
	t.Helper()
	r := baseReservation(id)
	r.OwnerEpoch = "epoch-dead"
	r.Steps = []Step{{Step: StepQuota, IdempotencyKey: id + ":quota", State: StepAcquired}}
	if pending == StepWorktree {
		r.LeaseIDs = []string{"lease-9"}
		r.Steps = append(r.Steps, Step{Step: StepLeases, IdempotencyKey: id + ":leases", Handle: "lease-9", State: StepAcquired})
	}
	r.Steps = append(r.Steps, Step{Step: pending, IdempotencyKey: id + ":" + string(pending), State: StepPending})
	stored, err := f.store.Insert(t.Context(), r)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return stored
}

// TestRecoverStepsLookupErrorLeavesRow: a FindLeases or FindWorktree
// error leaves the row byte-identical and is returned; a not-found
// result marks the step not_run and compensates nothing; NewReserver
// refuses a nil FindLeases, FindWorktree or RaiseAttention.
func TestRecoverStepsLookupErrorLeavesRow(t *testing.T) {
	f := newReserverFixture(t, 100000)
	lookup := errors.New("lease table unreadable")
	f.seams.FindLeases = func(context.Context, string, string, []string) ([]string, error) { return nil, lookup }
	f.seams.FindWorktree = func(context.Context, string) (string, bool, error) { return "", false, lookup }
	rv := f.reserver(t)
	for _, step := range []StepName{StepLeases, StepWorktree} {
		before := pendingRow(t, f, "r-"+string(step), step)
		_, err := rv.RecoverSteps(t.Context(), before.ID)
		if !hasIdentity(err, lookup) {
			t.Fatalf("RecoverSteps(%s lookup error) = %v, want the lookup error", step, err)
		}
		if after, _, _ := f.store.Get(t.Context(), before.ID); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("row changed after a %s lookup error:\n got %+v\nwant %+v", step, after, before)
		}
	}
	f.seams.FindLeases = func(context.Context, string, string, []string) ([]string, error) { return nil, nil }
	f.seams.FindWorktree = func(context.Context, string) (string, bool, error) { return "", false, nil }
	rv = f.reserverWithEpoch(t, "epoch-2")
	for _, step := range []StepName{StepLeases, StepWorktree} {
		got, err := rv.RecoverSteps(t.Context(), "r-"+string(step))
		if err != nil || findStep(got, step).State != StepNotRun || got.State != ReservationHeld {
			t.Fatalf("RecoverSteps(%s not found) = %+v, %v, want the step not_run on a still-held row", step, got, err)
		}
	}
	if calls := f.rec.snapshot(); len(calls) != 0 {
		t.Fatalf("not-found recovery compensated %v, want nothing", calls)
	}
	for name, mutate := range map[string]func(*ReserverSeams){
		"FindLeases":     func(s *ReserverSeams) { s.FindLeases = nil },
		"FindWorktree":   func(s *ReserverSeams) { s.FindWorktree = nil },
		"RaiseAttention": func(s *ReserverSeams) { s.RaiseAttention = nil },
	} {
		s := f.seams
		mutate(&s)
		if _, err := NewReserver(f.store, f.clock, "epoch-3", s); !isKindInvalidInput(err) {
			t.Errorf("NewReserver(nil %s) = %v, want KindInvalidInput", name, err)
		}
	}
}
