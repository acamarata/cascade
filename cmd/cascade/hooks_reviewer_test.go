//go:build !windows

// Purpose: the completion gate the daemon composition builds binds a
//
//	review job's accepted edge to the tree of its leased worktree, over a
//	real git repository, the daemon's Fleet, jobs store and provider
//	registry; a dirty worktree or an unreadable tree denies without a
//	Transition.
//
// SPORT: cmd/cascade/reviewer (ADD, P1-PBD-05).
package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewerGate is one composed daemon whose job-ci is a review job at
// reviewing with complete evidence, a passed acceptance re-run and a
// clean leased worktree.
type reviewerGate struct {
	w   *daemonWiring
	ref ci.JobRef
}

func newReviewerGate(t *testing.T) reviewerGate {
	t.Helper()
	ctx := context.Background()
	w := hookDaemon(t)
	if w.Deps.ProviderRegistry == nil {
		t.Fatal("composed daemon holds no provider registry reader")
	}
	ref := hookRecordedCheckpoint(t, w, newCIDaemonRig(t))
	hookEvidence(t, w, ref.JobID)
	passed, err := ciAcceptanceRerun(w.Deps.CI.Dispatcher(), ctx, ref, w.Deps.CI.Model(ref.RepoRoot))
	if err != nil || !passed.Passed {
		t.Fatalf("acceptance rerun = %+v, %v", passed, err)
	}
	j := hookStored(t, w, ref.JobID)
	j.MinTaskClass, j.MutableScope = "review", ""
	if err := w.Deps.Fleet.Jobs().PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	seedReviewerFamilies(t, w, ref.JobID)
	return reviewerGate{w: w, ref: ref}
}

// seedReviewerFamilies registers an anthropic and a gemini provider lane in
// the daemon's providers.db, and leases a code author of reviewID on the
// anthropic lane and reviewID itself on the gemini lane.
func seedReviewerFamilies(t *testing.T, w *daemonWiring, reviewID string) {
	t.Helper()
	ctx := context.Background()
	reg, closeReg := openLaneRegistry(t, w.Paths.DataDir(), w.Clock)
	defer closeReg()
	for name, driver := range map[string]providerregistry.DriverKind{"anthropic-acc1": providerregistry.DriverAnthropic, "gem-main": providerregistry.DriverGemini} {
		if err := reg.AddProvider(ctx, providerregistry.ProviderRecord{Name: name, Driver: driver, Auth: providerregistry.AuthKey,
			AuthRef: providerregistry.VaultKeyRef(name + "-ref"), KnownModels: []string{"m"}, AccountKind: providerregistry.AccountPersonal,
			Tier: providerregistry.TierFree, HealthStatus: providerregistry.HealthHealthy}); err != nil {
			t.Fatalf("AddProvider(%s): %v", name, err)
		}
		if err := reg.UpsertLane(ctx, providerregistry.LaneRecord{LaneName: "lane-" + name, ProviderName: name, Weight: 1,
			Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable}); err != nil {
			t.Fatalf("UpsertLane(%s): %v", name, err)
		}
	}
	store := w.Deps.Fleet.Jobs()
	tier := provider.SensitivityInternal
	if err := store.PutJob(ctx, jobs.Job{ID: "author", State: jobs.JobStateAccepted, CreatedAt: 1, UpdatedAt: 1,
		MutableScope: "docs/**", RiskClass: "normal", MinTaskClass: "code", ConsequenceClass: jobs.ConsequenceNormal, DataClass: &tier}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDependency(ctx, jobs.TaskDependency{JobID: reviewID, DependsOnJobID: "author"}); err != nil {
		t.Fatal(err)
	}
	reserveLane(t, w, "author", "author-exec", "lane-anthropic-acc1")
	reserveLane(t, w, reviewID, reviewID+"-review", "lane-gem-main")
}

// reserveLane records execID as jobID's latest execution and binds it to a
// reservation on lane in the Fleet's reservation ledger.
func reserveLane(t *testing.T, w *daemonWiring, jobID, execID, lane string) {
	t.Helper()
	ctx := context.Background()
	if err := w.Deps.Fleet.Jobs().PutExecution(ctx, jobs.Execution{ID: execID, JobID: jobID, Attempt: 99, State: jobs.ExecutionSucceeded}); err != nil {
		t.Fatalf("PutExecution(%s): %v", execID, err)
	}
	rsv, err := economics.NewReservationStore(w.Deps.Fleet.JobsDB(), w.Clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rsv.Insert(ctx, economics.Reservation{ID: "rsv-" + execID, ExecutionID: execID, JobID: jobID, LaneID: lane,
		Kind: economics.ReservationInteractive, State: economics.ReservationHeld}); err != nil {
		t.Fatalf("Insert(%s): %v", execID, err)
	}
}

// decisions lists the review job's reviewer decision rows.
func (g reviewerGate) decisions(t *testing.T) []jobs.ReviewerDecision {
	t.Helper()
	rows, err := g.w.Deps.Fleet.Jobs().ReviewerDecisions(context.Background(), g.ref.JobID)
	if err != nil {
		t.Fatalf("ReviewerDecisions: %v", err)
	}
	return rows
}

// wantDenied dispatches the composed completion check and requires a denial
// whose reason contains want, with the job row and the decision rows left
// byte-identical: Transition never ran.
func (g reviewerGate) wantDenied(t *testing.T, want string) {
	t.Helper()
	before, rows := hookStored(t, g.w, g.ref.JobID), g.decisions(t)
	resp := hookDispatch(t, g.w, g.ref.JobID)
	if !resp.Deny || !strings.Contains(resp.Reason, "reviewer worktree unreadable: ") || !strings.Contains(resp.Reason, want) {
		t.Fatalf("completion check = %+v, want a reviewer tree denial naming %q", resp, want)
	}
	if after := hookStored(t, g.w, g.ref.JobID); !reflect.DeepEqual(before, after) || after.State != jobs.JobStateReviewing {
		t.Fatalf("denial changed the job: before %+v after %+v", before, after)
	}
	if got := g.decisions(t); !reflect.DeepEqual(rows, got) {
		t.Fatalf("denial reached Transition: decision rows %+v -> %+v", rows, got)
	}
}

// wantAccepted dispatches the composed completion check and requires the
// job accepted with one assigned decision row over distinct families.
func (g reviewerGate) wantAccepted(t *testing.T) {
	t.Helper()
	resp := hookDispatch(t, g.w, g.ref.JobID)
	if resp.Deny || hookStored(t, g.w, g.ref.JobID).State != jobs.JobStateAccepted {
		t.Fatalf("clean review denied: %+v", resp)
	}
	rows := g.decisions(t)
	if len(rows) != 1 || rows[0].Outcome != jobs.ReviewOutcomeAssigned || !rows[0].FamilyDistinct ||
		rows[0].Reviewer != (jobs.FamilyRef{Family: "gemini", Resolved: true}) ||
		!reflect.DeepEqual(rows[0].Authors, []jobs.FamilyRef{{Family: "anthropic", Resolved: true}}) {
		t.Fatalf("decision rows = %+v, want one assigned anthropic/gemini row", rows)
	}
}

func TestCompletionGateFillsReviewerTree(t *testing.T) {
	g := newReviewerGate(t)
	tree := ciRigGit(t, g.ref.WorktreeRoot, "write-tree")
	snap, found, err := g.w.Deps.CI.Dispatcher().CurrentCheckpoint(context.Background(), g.ref.JobID)
	if err != nil || !found || snap.TreeHash != tree {
		t.Fatalf("checkpoint tree = %q (%v %v), worktree tree %q", snap.TreeHash, found, err, tree)
	}
	got, err := reviewerTreeFor(context.Background(), g.w.Deps.Fleet, hookStored(t, g.w, g.ref.JobID))
	if err != nil || got != tree {
		t.Fatalf("reviewerTreeFor = %q, %v; want %q", got, err, tree)
	}
	g.wantAccepted(t)
}

func TestCompletionGateDetectsUnstagedReviewerEdit(t *testing.T) {
	g := newReviewerGate(t)
	tracked := filepath.Join(g.ref.WorktreeRoot, "docs", "change.txt")
	writeCIFile(t, tracked, "reviewer edit\n")
	g.wantDenied(t, g.ref.WorktreeRoot)
	ciRigGit(t, g.ref.WorktreeRoot, "checkout", "--", "docs/change.txt")
	untracked := filepath.Join(g.ref.WorktreeRoot, "docs", "new.txt")
	writeCIFile(t, untracked, "new\n")
	g.wantDenied(t, g.ref.WorktreeRoot)
	if err := os.Remove(untracked); err != nil {
		t.Fatal(err)
	}
	g.wantAccepted(t)
}

func TestCompletionGateReviewerTreeErrorDenies(t *testing.T) {
	g := newReviewerGate(t)
	ctx, leases := context.Background(), g.w.Deps.Fleet.Leases()
	extra, err := leases.Acquire(ctx, g.ref.Lease.RepoID, "other/**", g.ref.JobID)
	if err != nil || !extra.Granted {
		t.Fatalf("second lease = %+v, %v", extra, err)
	}
	g.wantDenied(t, "multiple active reviewer leases")
	if err := leases.Release(ctx, extra.Lease.RepoID, extra.Lease.ScopeGlob, extra.Lease.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := leases.Release(ctx, g.ref.Lease.RepoID, g.ref.Lease.ScopeGlob, g.ref.Lease.Epoch); err != nil {
		t.Fatal(err)
	}
	g.wantDenied(t, "no active reviewer lease")
	if _, err := reviewerTreeFor(ctx, nil, hookStored(t, g.w, g.ref.JobID)); err == nil {
		t.Fatal("reviewerTreeFor without a fleet returned a tree")
	}
}
