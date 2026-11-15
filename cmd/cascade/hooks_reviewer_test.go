//go:build !windows

// Purpose: the completion gate the daemon composition builds binds a review
// job's accepted edge to its leased worktree tree and to the family ladder,
// over a real git repository and the daemon's Fleet, jobs store and provider
// registry; a dirty or unreadable tree denies without a Transition.
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
	"github.com/acamarata/cascade/internal/review"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewerGate is one composed daemon whose job-ci is a review job at
// reviewing with complete evidence, a passed acceptance re-run and a
// clean leased worktree.
type reviewerGate struct {
	w      *daemonWiring
	ref    ci.JobRef
	ledger *jobs.EvidenceLedger
	auth   jobs.AppendAuthorization
}

func newReviewerGate(t *testing.T) reviewerGate {
	t.Helper()
	ctx, w := context.Background(), hookDaemon(t)
	if w.Deps.ProviderRegistry == nil {
		t.Fatal("composed daemon holds no provider registry reader")
	}
	ref := hookRecordedCheckpoint(t, w, newCIDaemonRig(t))
	ledger, auth := hookEvidence(t, w, ref.JobID)
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
	return reviewerGate{w: w, ref: ref, ledger: ledger, auth: auth}
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
	reserveLane(t, w, "author", "author-exec", "lane-anthropic-acc1", 99)
	reserveLane(t, w, reviewID, reviewID+"-review", "lane-gem-main", 99)
}

// reserveLane records execID at attempt as jobID's latest execution and
// binds it to a reservation on lane in the Fleet's reservation ledger.
func reserveLane(t *testing.T, w *daemonWiring, jobID, execID, lane string, attempt int) {
	t.Helper()
	ctx := context.Background()
	if err := w.Deps.Fleet.Jobs().PutExecution(ctx, jobs.Execution{ID: execID, JobID: jobID, Attempt: attempt, State: jobs.ExecutionSucceeded}); err != nil {
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

// attempt stores risk on the review job and dispatches the composed
// completion check; it returns the reason and whether the check denied.
func (g reviewerGate) attempt(t *testing.T, risk string) (reason string, denied bool) {
	t.Helper()
	j := hookStored(t, g.w, g.ref.JobID)
	j.RiskClass = risk
	if err := g.w.Deps.Fleet.Jobs().PutJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	resp := hookDispatch(t, g.w, g.ref.JobID)
	return resp.Reason, resp.Deny
}

// The author moves to lane at a later attempt: lane-gem-main shares the
// reviewer's driver. The gate feeds no change footprint, so a planned low
// class derives normal and is denied as escalated before any reviewer row.
func TestCompletionGateFamilyLadder(t *testing.T) {
	cases := []struct{ name, risk, lane, outcome, reason string }{
		{"high-shared", "high", "lane-gem-main", jobs.ReviewOutcomeHeld, jobs.ErrNoDistinctFamily.Error()},
		{"normal-shared", "normal", "lane-gem-main", jobs.ReviewOutcomeDegraded, jobs.ErrHumanApprovalRequired.Error()},
		{"low-shared", "low", "lane-gem-main", "", "risk escalated: low -> normal"},
		{"high-distinct", "high", "lane-anthropic-acc1", jobs.ReviewOutcomeAssigned, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newReviewerGate(t)
			reserveLane(t, g.w, "author", "author-exec2", c.lane, 100)
			reason, denied := g.attempt(t, c.risk)
			rows, state := g.decisions(t), hookStored(t, g.w, g.ref.JobID).State
			if denied != (c.outcome != jobs.ReviewOutcomeAssigned) || denied != (state == jobs.JobStateReviewing) ||
				!strings.Contains(reason, c.reason) || len(rows) > 1 || (len(rows) == 1) != (c.outcome != "") {
				t.Fatalf("denied %v state %s reason %q rows %+v", denied, state, reason, rows)
			}
			if len(rows) == 1 && (rows[0].Outcome != c.outcome || rows[0].FamilyDistinct != (c.lane != "lane-gem-main")) {
				t.Fatalf("decision row = %+v, want outcome %s", rows[0], c.outcome)
			}
		})
	}
}

func TestComposedGateHasReviewerEnforcement(t *testing.T) {
	w := hookDaemon(t)
	ctx, rt := context.Background(), w.Deps.Fleet
	var n int
	if err := rt.JobsDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'jobs_reviewer_decision'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("daemon jobs handle has %d jobs_reviewer_decision tables (%v)", n, err)
	}
	en, err := composeReviewer(ctx, w.Clock, reviewerSources{fleet: rt, registry: w.Deps.ProviderRegistry})
	if err != nil || en == nil {
		t.Fatalf("composeReviewer = %v, %v", en, err)
	}
	policy, err := en.Policy(jobs.RiskClassHigh)
	if _, ok := policy.(review.JobReviewPolicy); err != nil || !ok {
		t.Fatalf("Policy(high) = %T, %v; want review.JobReviewPolicy", policy, err)
	}
	in, ok := en.Inputs.(reviewerInputsByID)
	if !ok || in.in.fleet != rt.Fleet() || in.in.jobs != rt.Jobs() || in.in.reg != w.Deps.ProviderRegistry || en.Decisions != rt.Jobs() {
		t.Fatalf("Inputs = %#v, Decisions = %#v; want the daemon fleet, jobs store and registry", en.Inputs, en.Decisions)
	}
	bare, err := composeReviewer(ctx, w.Clock, reviewerSources{fleet: rt})
	if err != nil || bare == nil || bare.Inputs != nil {
		t.Fatalf("without a registry Inputs = %#v, %v; want nil so the gate denies", bare, err)
	}
}

// A mutated-tree denial records no row (Transition never ran); a held and a
// degraded attempt on a shared driver and an accepted one on distinct
// drivers each record one, and the review evidence binds the accepted row.
func TestReviewerDecisionRowsOnDaemon(t *testing.T) {
	g, ctx := newReviewerGate(t), context.Background()
	writeCIFile(t, filepath.Join(g.ref.WorktreeRoot, "docs", "change.txt"), "reviewer edit\n")
	g.wantDenied(t, g.ref.WorktreeRoot)
	ciRigGit(t, g.ref.WorktreeRoot, "checkout", "--", "docs/change.txt")
	reserveLane(t, g.w, "author", "author-exec2", "lane-gem-main", 100)
	var row jobs.ReviewerDecision
	for i, outcome := range []string{jobs.ReviewOutcomeHeld, jobs.ReviewOutcomeDegraded, jobs.ReviewOutcomeAssigned} {
		if i == 2 {
			reserveLane(t, g.w, "author", "author-exec3", "lane-anthropic-acc1", 101)
		}
		reason, denied := g.attempt(t, map[int]string{0: "high", 1: "normal", 2: "high"}[i])
		rows := g.decisions(t)
		for _, r := range rows {
			if r.Outcome == outcome {
				row = r
			}
		}
		if len(rows) != i+1 || row.Outcome != outcome || denied != (i < 2) || (denied && !strings.Contains(reason, jobs.ReviewEvidenceRef(row))) {
			t.Fatalf("attempt %d: denied %v reason %q rows %+v, want %s", i+1, denied, reason, rows, outcome)
		}
	}
	rec := hookRecord(g.ref.JobID, jobs.EvidenceReview, "review-decision")
	rec.ArtifactRef = jobs.ReviewEvidenceRef(row)
	if _, err := g.ledger.Append(ctx, rec, g.auth); err != nil {
		t.Fatalf("append review evidence: %v", err)
	}
	got, err := g.ledger.Query(ctx, g.ref.JobID, jobs.EvidenceReview)
	if err != nil || got.ArtifactRef != "reviewer-decision:"+row.ID || row.ID == "" {
		t.Fatalf("review evidence = %+v, %v; want ArtifactRef reviewer-decision:%s", got, err, row.ID)
	}
}
