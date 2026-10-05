// Purpose: the checkpoint's refusal gates: a stale lease epoch writes
// nothing and raises one attention item, the sensitivity floor refuses
// before any effect, and the tree binding refuses an index that differs
// from the commit by anything but declared untracked paths.
//
// SPORT: internal.ci.fence/TESTED (P1-CI-01).
package ci

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestCheckpointInvalidRefWritesNothing(t *testing.T) {
	r := newRig(t)
	r.ref.CheckpointCommit = r.base
	for _, tc := range []struct {
		message string
		change  func(*JobRef)
	}{
		{"requires a JobID", func(ref *JobRef) { ref.JobID = "" }},
		{"requires a RepoRoot", func(ref *JobRef) { ref.RepoRoot = "" }},
		{"requires a Lease", func(ref *JobRef) { ref.Lease.RepoID = "" }},
		{"requires a Lease", func(ref *JobRef) { ref.Lease.ScopeGlob = "" }},
		{"not a risk class", func(ref *JobRef) { ref.PlannedRisk = "unknown" }},
		{"not a commit id", func(ref *JobRef) { ref.CheckpointCommit = "--help" }},
	} {
		t.Run(tc.message, func(t *testing.T) {
			ref := r.ref
			tc.change(&ref)
			_, err := r.d.Checkpoint(context.Background(), ref, RequirementModel{})
			assertStreamError(t, err, cascade.KindInvalidInput, tc.message)
			if run, outbox, attempt := r.zeroRows(); run != 0 || outbox != 0 || attempt != 0 {
				t.Fatalf("invalid ref wrote rows: %d/%d/%d", run, outbox, attempt)
			}
		})
	}
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("valid ref: %v", err)
	}
	if _, outbox, attempt := r.zeroRows(); outbox == 0 || attempt != 1 {
		t.Fatalf("valid ref did not persist dispatch: %d/%d", outbox, attempt)
	}
}

func TestBindTreeRefusesUnreadableObjects(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.ref.CheckpointCommit = "abcdef0123456789"
	_, err := r.d.bindTree(ctx, r.ref, "")
	assertStreamError(t, err, cascade.KindIntegrity, "has no readable tree")
	r.ref.CheckpointCommit, r.ref.Untracked = r.base, []string{"docs/"}
	_, err = r.d.bindTree(ctx, r.ref, "invalid")
	assertStreamError(t, err, cascade.KindIntegrity, "snapshot returned no tree hash")
	if !errChainHas(err, ErrTreeHashMismatch) {
		t.Fatalf("lost mismatch sentinel: %v", err)
	}
	_, err = r.d.bindTree(ctx, r.ref, "abcdef0123456789")
	assertStreamError(t, err, cascade.KindIntegrity, "diffing the snapshot")
	tree := treeOf(t, r.repo, r.base)
	if got, err := r.d.bindTree(ctx, r.ref, tree); err != nil || got != tree {
		t.Fatalf("readable identical tree: %q, %v", got, err)
	}
}

func TestFencePreservesCauseWhenAttentionFails(t *testing.T) {
	r := newRig(t)
	r.attn.failWith = errors.New("attention store offline")
	r.ref.LeaseEpoch++
	err := r.d.fence(context.Background(), r.ref)
	assertFenced(t, err)
	if !errors.Is(err, cascade.ErrUnavailable) || !strings.Contains(err.Error(), "raising the attention item") {
		t.Fatalf("missing attention failure taxonomy: %v", err)
	}
	if !errChainHas(err, r.attn.failWith) {
		t.Fatalf("attention failure lost: %v", err)
	}
	cause := errors.New("lease store offline")
	r.d.deps.Fence = func(context.Context, string, string, int64) error { return cause }
	if got := r.d.fence(context.Background(), r.ref); got != cause {
		t.Fatalf("non-fence failure replaced: %v", got)
	}
	if errChainHas(errors.Join(cause, r.attn.failWith), ErrCheckpointStale) {
		t.Fatal("unrelated joined errors matched a stale checkpoint")
	}
}

// zeroRows returns the row counts a refused checkpoint must leave at zero.
func (r *streamRig) zeroRows() (run, outbox, attempt int) {
	return r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run`),
		r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox`),
		r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt`)
}

func assertFenced(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("stale epoch: want jobs.ErrLeaseFenced, got nil")
	}
	if !errChainHas(err, jobs.ErrLeaseFenced) {
		t.Fatalf("err = %v, want the identity of jobs.ErrLeaseFenced in its chain", err)
	}
	if !strings.Contains(err.Error(), "lease epoch fence mismatch") {
		t.Fatalf("err = %q, want the fence's own message", err)
	}
}

func TestLeaseFenceRefusesStaleEpoch(t *testing.T) {
	variants := map[string]bool{"dispatcher fence alone (non-fencing snapshot)": true, "real worktree snapshot": false}
	for name, isolate := range variants {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.useLocal(&lockedExec{}, nil)
			if isolate {
				r.snap = func(context.Context, jobs.FenceFunc, jobs.ResourceLease, int64, []string) (jobs.SnapshotResult, error) {
					return jobs.SnapshotResult{TreeHash: "unused"}, nil
				}
				r.useLocal(&lockedExec{}, nil)
			}
			r.commit(map[string]string{"docs/a.md": "a"})

			stale := r.ref
			stale.LeaseEpoch = 99
			_, err := r.d.Checkpoint(context.Background(), stale, RequirementModel{})
			assertFenced(t, err)
			run, outbox, attempt := r.zeroRows()
			if run != 0 || outbox != 0 || attempt != 0 {
				t.Fatalf("stale epoch wrote rows: ci_run=%d outbox=%d attempt=%d, want all zero", run, outbox, attempt)
			}
			if got := len(r.attn.pushed); got != 1 {
				t.Fatalf("attention items = %d, want exactly 1", got)
			}
			l, ok, gerr := r.store.GetLease(context.Background(), r.lease.RepoID, r.lease.ScopeGlob)
			if gerr != nil || !ok || l.Epoch != 1 {
				t.Fatalf("lease after refusal = %+v ok=%v err=%v, want epoch unchanged at 1", l, ok, gerr)
			}

			if _, err := r.checkpoint(); err != nil {
				t.Fatalf("positive control (current epoch): %v", err)
			}
			run, outbox, attempt = r.zeroRows()
			if run == 0 || outbox == 0 || attempt != 1 {
				t.Fatalf("positive control wrote ci_run=%d outbox=%d attempt=%d, want rows in each", run, outbox, attempt)
			}
		})
	}
}

func TestSensitivityTravels(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	r.ref.Sensitivity = provider.SensitivityLocalOnly
	r.stored = provider.SensitivityRestricted
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	var tier string
	if err := r.ciDB.QueryRow(`SELECT sensitivity FROM ci_run_stream WHERE checkpoint_id = ? LIMIT 1`, snap.CheckpointID).Scan(&tier); err != nil || tier != "local-only" {
		t.Fatalf("ci_run_stream sensitivity = %q, err %v; want local-only", tier, err)
	}
	for _, kind := range []events.EventKind{EventKindCheckpointDispatched, EventKindCheckpointTerminal} {
		evs := r.streamEvents(kind)
		if len(evs) == 0 {
			t.Fatalf("no %s events (positive control)", kind)
		}
		for _, p := range evs {
			if p.Sensitivity != "local-only" {
				t.Fatalf("%s payload sensitivity = %q, want local-only", kind, p.Sensitivity)
			}
		}
	}

	rec := newRig(t)
	rec.ref.Sensitivity = provider.SensitivityPublic
	rec.stored = provider.SensitivityInternal
	rec.commit(map[string]string{"docs/a.md": "a"})
	_, err = rec.checkpoint()
	if !errChainHas(err, ErrSensitivityLowered) {
		t.Fatalf("lowered tier: err = %v, want ErrSensitivityLowered", err)
	}
	if run, outbox, attempt := rec.zeroRows(); run+outbox+attempt != 0 || rec.exec.count() != 0 {
		t.Fatalf("lowered tier had effects: rows %d/%d/%d, executor runs %d", run, outbox, attempt, rec.exec.count())
	}
	rec.ref.Sensitivity = provider.SensitivityInternal
	if _, err := rec.checkpoint(); err != nil {
		t.Fatalf("positive control (equal tier): %v", err)
	}
	if rec.exec.count() == 0 {
		t.Fatal("positive control dispatched nothing")
	}
	rec.d.deps.StoredSensitivity = func(context.Context, string) (provider.SensitivityTier, error) {
		return 0, errors.New("store down")
	}
	rec.ref.CheckpointCommit = rec.commit(map[string]string{"docs/b.md": "b"})
	if _, err := rec.checkpoint(); err == nil {
		t.Fatal("unreadable stored tier must refuse")
	}
}

func TestBindTreeRefusesStagedEditsAroundUntracked(t *testing.T) {
	r := newRig(t)
	r.commit(map[string]string{"docs/a.md": "a"})
	writeRepoFile(t, r.wt, "notes.txt", "declared")
	r.ref.Untracked = []string{"notes.txt"}
	snapRes, err := r.wm.Snapshot(context.Background(), r.lm.Fence, r.lease, 1, r.ref.Untracked)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	tree, err := r.d.bindTree(context.Background(), r.ref, snapRes.TreeHash)
	if err != nil || tree != snapRes.TreeHash {
		t.Fatalf("bindTree with only the declared path differing = %q, %v; want the snapshot tree", tree, err)
	}
	writeRepoFile(t, r.wt, "docs/a.md", "edited and staged")
	runGit(t, r.wt, "add", "docs/a.md")
	dirty, err := r.wm.Snapshot(context.Background(), r.lm.Fence, r.lease, 1, r.ref.Untracked)
	if err != nil || dirty.TreeHash == snapRes.TreeHash {
		t.Fatalf("positive control: staging an edit must change the index tree (%v)", err)
	}
	_, err = r.d.bindTree(context.Background(), r.ref, dirty.TreeHash)
	if !errChainHas(err, ErrTreeHashMismatch) || !strings.Contains(err.Error(), "docs/a.md") {
		t.Fatalf("bindTree over a staged edit: err = %v, want ErrTreeHashMismatch naming docs/a.md", err)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want integrity", k)
	}
}
