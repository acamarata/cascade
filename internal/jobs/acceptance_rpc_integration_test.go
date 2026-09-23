//go:build integration

package jobs_test

// Purpose: P1-E29-W6-S60-T4 Path 4 -- job.list/lease.list over the
//
//	daemon's REAL unix socket, exercising the S-60.T1 CLI/RPC surface
//	after Paths 1-3 established the lifecycle.
//
// WHY THIS FILE IS `//go:build integration` (AMD-20260922/F3-14, R-14.305,
// register A1-162): internal/build/hygiene.go's NoNetworkUnitTestScanFile
// refuses `net`/`net/http` in any untagged `_test.go` file, and a real
// unix-socket RPC dial has no lower-level standard-library primitive
// outside package `net`. This ticket's own `checks:` list runs this
// file's test alongside Path 2's in ONE combined `-tags integration` run
// (`go test -race -count=1 -tags integration -run
// '^(TestAcceptancePath2KillResume|TestAcceptancePath4RPCSpotCheck)$'
// ./internal/jobs/`, R-14.313's consolidated rewrite), never the plain
// `-run TestAcceptance` line Paths 1/3 share.
//
// This file reuses acceptance_resume_integration_test.go's TestMain/
// path2Home/newPath2Home/spawnDaemon/sigkillAndWait/dialRPC and
// acceptance_resume_integration_rig_test.go's path2Rig/openPath2Rig/
// appendPath2Evidence -- all `//go:build integration`, so they compile
// into this SAME test binary alongside Path 2 (built once, reused by
// every integration path).
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

const path4JobID = "job-path4-rpc"
const path4Repo = "path4-repo"

// seedPath4Accepted drives a fresh job the REAL package APIs (the same
// pending->leased->running->verifying->reviewing->accepted lifecycle
// acceptance_path1_test.go establishes) all the way to `accepted`, then
// EXPLICITLY releases its lease -- so job.list(state:accepted) has a real
// row to find and lease.list has zero active leases left, over the SAME
// cascade.db the spawned daemon serves.
func seedPath4Accepted(t *testing.T, dbPath string, clock runtime.Clock, repoRoot string) {
	t.Helper()
	rig := openPath2Rig(t, dbPath, clock)
	defer func() { _ = rig.db.Close() }()
	ctx := nodes.WithRole(context.Background(), nodes.RoleController)

	if err := rig.store.PutJob(ctx, jobs.Job{
		ID: path4JobID, State: jobs.JobStatePending, MutableScope: "docs/**",
		RiskClass: string(jobs.RiskClassLow), MinTaskClass: "code",
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	acquired, err := rig.leases.Acquire(ctx, path4Repo, "docs/**", path4JobID)
	if err != nil || !acquired.Granted {
		t.Fatalf("Acquire: %+v, %v", acquired, err)
	}
	if _, err := rig.worktree.Create(ctx, acquired.Lease, repoRoot); err != nil {
		t.Fatalf("worktree Create: %v", err)
	}
	if err := rig.store.PutTransition(ctx, path4JobID, jobs.JobStateLeased, 1); err != nil {
		t.Fatalf("PutTransition ->leased: %v", err)
	}
	if err := rig.store.PutTransition(ctx, path4JobID, jobs.JobStateRunning, 2); err != nil {
		t.Fatalf("PutTransition ->running: %v", err)
	}
	if err := rig.store.PutExecution(ctx, jobs.Execution{ID: "exec-" + path4JobID, JobID: path4JobID, Attempt: 1, State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	appendPath4Evidence(t, rig, acquired.Lease, jobs.EvidenceLint, "idem-path4-lint-1")
	appendPath4Evidence(t, rig, acquired.Lease, jobs.EvidenceTests, "idem-path4-tests-1")
	driveP4ToAcceptedAndRelease(ctx, t, rig, acquired.Lease)
}

// driveP4ToAcceptedAndRelease is seedPath4Accepted's completion half
// (Art.10.3 funlen split, no new concern): running->verifying->
// reviewing->accepted, then an EXPLICIT release -- CompletionPolicy.commit
// does not call Store.PutTransition (see acceptance_test.go's own
// CONTRACT NOTE precedent), so nothing releases this lease automatically.
func driveP4ToAcceptedAndRelease(ctx context.Context, t *testing.T, rig *path2Rig, lease jobs.ResourceLease) {
	t.Helper()
	job, ok, err := rig.store.GetJob(ctx, path4JobID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	req := func(target jobs.JobState) jobs.TransitionRequest {
		return jobs.TransitionRequest{
			Job: &job, Target: target, Caller: jobs.PolicyEngineIdentity{EngineID: acceptanceEngineID},
			PlannedRiskClass:   jobs.RiskClassLow,
			ActualFootprint:    jobs.ChangeFootprint{ChangedPaths: []string{"docs/fixture-a.md", "docs/fixture-b.md"}},
			LeaseScopePrefixes: []string{"docs/"},
		}
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateVerifying)); err != nil {
		t.Fatalf("Transition ->verifying: %v", err)
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateReviewing)); err != nil {
		t.Fatalf("Transition ->reviewing: %v", err)
	}
	appendPath4Evidence(t, rig, lease, jobs.EvidenceReview, "idem-path4-review-1")
	if err := rig.policy.Transition(ctx, req(jobs.JobStateAccepted)); err != nil {
		t.Fatalf("Transition ->accepted: %v", err)
	}
	if err := rig.leases.Release(ctx, path4Repo, "docs/**", lease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// appendPath4Evidence mirrors acceptance_path2_rig_test.go's
// appendPath2Evidence for path4JobID.
func appendPath4Evidence(t *testing.T, rig *path2Rig, lease jobs.ResourceLease, kind jobs.EvidenceKind, idem string) {
	t.Helper()
	rec := jobs.EvidenceRecord{
		JobID: path4JobID, Kind: kind, ProducerCapability: jobs.ProducerControllerRun,
		AttemptID: "exec-" + path4JobID, AttestorIdentity: "daemon:acceptance",
		Outcome: jobs.OutcomePass, IdempotencyKey: idem,
	}
	auth := jobs.AppendAuthorization{
		ExecutionID: "exec-" + path4JobID, LeaseRepoID: lease.RepoID,
		LeaseScopeGlob: lease.ScopeGlob, LeaseEpoch: lease.Epoch,
	}
	if _, err := rig.ledger.Append(context.Background(), rec, auth); err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
}

// dialRPC POSTs one JSON-RPC request to sockPath over a REAL unix socket
// (the exact dial pattern cmd/cascade/daemon_unix_journal_integration_test.go
// already establishes: a real http.Client with a unix DialContext) and
// decodes result into out.
func dialRPC(t *testing.T, sockPath, method string, params any, out any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "acceptance-path4", "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s (%s) over the real socket: %v", rpc.RPCPath, method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s (%s) -> %d, want 200", rpc.RPCPath, method, resp.StatusCode)
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	if envelope.Error != nil {
		t.Fatalf("%s over the daemon socket returned an error: %d %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			t.Fatalf("decode %s result: %v", method, err)
		}
	}
}

// TestAcceptancePath4RPCSpotCheck is the RPC spot-check: after driving a job to
// `accepted` and releasing its lease (seedPath4Accepted, over the SAME
// cascade.db the daemon serves), job.list(state:accepted) over the REAL
// unix socket finds it, and lease.list shows zero active leases.
func TestAcceptancePath4RPCSpotCheck(t *testing.T) {
	home := newPath2Home(t)
	realClock := runtime.NewSystemClock()

	seedPath4Accepted(t, home.dbPath, realClock, home.repoRoot)

	daemon := home.spawnDaemon(t)
	defer sigkillAndWait(t, daemon)

	var jobsResp struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	dialRPC(t, home.sockPath, "job.list", map[string]any{"state": "accepted"}, &jobsResp)
	found := false
	for _, j := range jobsResp.Jobs {
		if j.ID == path4JobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("job.list(state:accepted) = %+v, want it to contain %q", jobsResp.Jobs, path4JobID)
	}

	// lease.list (internal/rpc/jobs.go's handleLeaseList -> jobs.Store.
	// ListLeases) returns every row regardless of state -- released rows
	// are NOT filtered out of the page (the scheduler's own admission
	// check filters by LeaseState.Contending() instead, model.go's own
	// closed-vocabulary precedent; acceptance_path3_test.go's
	// activeLeasesForRepo relies on that same "list is unfiltered, the
	// CALLER decides what counts as active" contract). "Zero active
	// leases" is therefore checked by state, matching the ticket's own
	// wording, not by an empty response.
	var leasesResp struct {
		Leases []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"leases"`
	}
	dialRPC(t, home.sockPath, "lease.list", map[string]any{}, &leasesResp)
	for _, l := range leasesResp.Leases {
		if l.State == "held" || l.State == "renewing" {
			t.Fatalf("lease.list after release = %+v, want zero active (held/renewing) leases", leasesResp.Leases)
		}
	}
}
