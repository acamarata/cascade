//go:build !windows

// Purpose: conductor.delegate binds a mutation to its job's worktree: the
// owner hop's roots are the worktree root joined with the job's mutable
// scope, and a job without a held worktree, or an unknown job, is refused.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// seedJobWorktree stores job id with mutable scope a/** and, when wt is set, a
// held lease bound to the worktree at wt.
func seedJobWorktree(ctx context.Context, t *testing.T, store *jobs.Store, id, wt string) {
	t.Helper()
	err := store.PutJob(ctx, jobs.Job{ID: id, State: jobs.JobStateRunning, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "a/**", RiskClass: "normal", MinTaskClass: "code",
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: new(provider.SensitivityInternal)})
	if err == nil && wt != "" {
		err = store.PutLease(ctx, jobs.ResourceLease{RepoID: "repo-" + id, ScopeGlob: "a/**", Holder: id, IssuedAt: 1,
			TTLSeconds: jobs.DefaultLeaseDefaults().TTLSeconds, Epoch: 1, State: jobs.LeaseHeld})
	}
	if err == nil && wt != "" {
		err = store.PutWorktree(ctx, jobs.Worktree{Path: wt, LeaseRepoID: "repo-" + id, LeaseScopeGlob: "a/**",
			Repo: filepath.Dir(wt), Branch: "job/" + id})
	}
	if err != nil {
		t.Fatal(err)
	}
}

// openBinderStore opens a one-connection store with the jobs schema.
func openBinderStore(ctx context.Context, t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewFixedClock(delegateT0), "", ""); err != nil {
		t.Fatal(err)
	}
	return jobs.NewStore(db)
}

func TestDelegateMutationBindsJobWorktree(t *testing.T) {
	ctx := context.Background()
	f := newDelegateFixture(t)
	f.rt.binder = openBinderStore(ctx, t)
	wt := filepath.Join(t.TempDir(), "wt-j1")
	seedJobWorktree(ctx, t, f.rt.binder, "j1", wt)
	seedJobWorktree(ctx, t, f.rt.binder, "j2", "")
	h := delegateHandlers{rt: f.rt}
	res, err := h.delegate(delegateOwner(), json.RawMessage(`{"intent":"x","mutation":true,"job_id":"j1"}`))
	got, ok := res.(delegateResult)
	if err != nil || !ok {
		t.Fatalf("mutation = %#v, %v; want dispatched", res, err)
	}
	f.manifest.Wait()
	f.notify.Dispatcher.Drain(ctx)
	rec, err := f.rt.store.GetDelegation(ctx, got.JobID)
	var signed scopeenvelope.Signed
	if err == nil {
		err = json.Unmarshal(rec.Signed, &signed)
	}
	if err != nil || !rec.Mutation || rec.JobID != "j1" || !slices.Equal(signed.Env.Roots, []string{filepath.Join(wt, "a")}) {
		t.Fatalf("record = %+v roots %v (%v); want a j1 mutation rooted at %s/a", rec, signed.Env.Roots, err, wt)
	}
	if _, err := h.delegate(delegateOwner(), json.RawMessage(`{"intent":"x","mutation":true,"job_id":"j2"}`)); err != errDelegateMutationUnbound {
		t.Fatalf("job without a worktree = %v; want errDelegateMutationUnbound", err)
	}
	if _, err := h.delegate(delegateOwner(), json.RawMessage(`{"intent":"x","mutation":true,"job_id":"nope"}`)); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown job = %v; want KindNotFound", err)
	}
}
