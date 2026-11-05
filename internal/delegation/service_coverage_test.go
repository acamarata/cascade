// Purpose: branch tests for the delegation service and worktree allocator (error returns, refusals,
//   empty inputs) that keep internal/delegation at its coverage ratchet; split from service_test.go
//   to hold it under the 300-line file cap.

package delegation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestCoverageGapDelegation covers remaining untested branches in service.go and worktree.go.
func TestCoverageGapDelegation(t *testing.T) {
	t.Run("empty_agent_id", func(t *testing.T) {
		_, good, svc, f, _ := setup(t)
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "", Parent: good})
		// Empty AgentID causes newPrincipalID to return "", triggering ErrSpawnBinding after a lease is acquired.
		failIf(t, resp != nil || err != ErrSpawnBinding || len(f.refs) != 1 || strings.Join(f.calls, ",") != "abort",
			"empty agent: Delegate = %+v, %v with %d leases, calls %v; want ErrSpawnBinding and abort", resp, err, len(f.refs), f.calls)
		wantCascadeErr(t, err, cascade.KindInternal, "the spawn carries no lease id or child principal")
	})

	t.Run("nil_request", func(t *testing.T) {
		_, _, svc, f, _ := setup(t)
		resp, err := svc.Delegate(context.Background(), nil)
		failIf(t, resp != nil || err == nil || len(f.refs) != 0, "nil request: Delegate = %+v, %v with %d leases; want error and no lease", resp, err, len(f.refs))
	})

	t.Run("missing_store", func(t *testing.T) {
		c := newChain(t)
		_, good := c.add(c.root)
		svc := NewService(Deps{Clock: fakeClock{now: t0}, Pinned: c.s.PublicKey(), Signer: c.s, fleet: &fleetDouble{}})
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good})
		failIf(t, resp != nil || err == nil, "missing store: Delegate = %+v, %v; want error", resp, err)
		wantCascadeErr(t, err, cascade.KindInvalidInput, "store")
	})

}

// TestCoverageGapDelegationMore holds the second half of the delegation branch tests.
func TestCoverageGapDelegationMore(t *testing.T) {
	t.Run("missing_signer", func(t *testing.T) {
		c := newChain(t)
		_, good := c.add(c.root)
		svc := NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), fleet: &fleetDouble{}})
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good})
		failIf(t, resp != nil || err == nil, "missing signer: Delegate = %+v, %v; want error", resp, err)
		wantCascadeErr(t, err, cascade.KindInvalidInput, "signer")
	})

	t.Run("missing_clock", func(t *testing.T) {
		c := newChain(t)
		_, good := c.add(c.root)
		svc := NewService(Deps{Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s, fleet: &fleetDouble{}})
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good})
		failIf(t, resp != nil || err == nil, "missing clock: Delegate = %+v, %v; want error", resp, err)
		wantCascadeErr(t, err, cascade.KindInvalidInput, "clock")
	})

	t.Run("shim_error_aborts", func(t *testing.T) {
		c, good, _, _, _ := setup(t)
		f := &fleetDouble{}
		badShim := &errShimDouble{err: errors.New("spawn boom")}
		svc := NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s,
			Minter: principal.NewMinter(fakeClock{now: t0}), Shim: badShim, fleet: f})
		svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "shim-fail", Parent: good})
		failIf(t, resp != nil || err == nil || !strings.Contains(err.Error(), "spawn boom") || strings.Join(f.calls, ",") != "abort",
			"shim error: Delegate = %+v, %v, calls %v; want abort and spawn error", resp, err, f.calls)
		wantRefused(t, c, "shim-fail", err)
	})

	t.Run("worktree_empty_jobid", func(t *testing.T) {
		base := t.TempDir()
		wt, err := AllocWorktree(base, "", true)
		failIf(t, wt != nil || err == nil, "AllocWorktree empty job = %+v, %v; want error", wt, err)
	})
}

type errShimDouble struct{ err error }

func (e *errShimDouble) Spawn(_ context.Context, _ agents.SpawnRequest) (agents.SpawnHandle, error) {
	return agents.SpawnHandle{}, e.err
}

// preBindShim binds the spawn in the store itself, so the service's own bind is refused.
type preBindShim struct {
	st       *jobs.Store
	released bool
}

func (p *preBindShim) Spawn(ctx context.Context, r agents.SpawnRequest) (agents.SpawnHandle, error) {
	if err := p.st.BindDelegationSpawn(ctx, string(r.Job.JobID), r.LeaseID, "pre-bound"); err != nil {
		return agents.SpawnHandle{}, err
	}
	return agents.SpawnHandle{JobID: r.Job.JobID, Release: func(context.Context) error { p.released = true; return nil }}, nil
}

// TestDelegateServiceSeamRefusals drives the Delegate seam checks (fleet, shim, minter, principal id).
func TestDelegateServiceSeamRefusals(t *testing.T) {
	cases := []struct {
		name, want string
		kind       cascade.Kind
		calls      string
		mutate     func(d *Deps, f *fleetDouble)
	}{
		{"no_fleet", "a fleet is required", cascade.KindInvalidInput, "", func(d *Deps, _ *fleetDouble) { d.fleet = nil }},
		{"no_shim", "a shim and a minter are required", cascade.KindInvalidInput, "abort", func(d *Deps, _ *fleetDouble) { d.Shim = nil }},
		{"no_minter", "a shim and a minter are required", cascade.KindInvalidInput, "abort", func(d *Deps, _ *fleetDouble) { d.Minter = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChain(t)
			_, good := c.add(c.root)
			f := &fleetDouble{}
			d := Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s,
				Minter: principal.NewMinter(fakeClock{now: t0}), Shim: &shimDouble{}, fleet: f}
			tc.mutate(&d, f)
			svc := NewService(d)
			svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
			resp, err := svc.Delegate(context.Background(), &Request{AgentID: "seam-" + tc.name, Parent: good})
			failIf(t, resp != nil || err == nil || strings.Join(f.calls, ",") != tc.calls, "%s: Delegate = %+v, %v, calls %v; want error and calls %q", tc.name, resp, err, f.calls, tc.calls)
			wantCascadeErr(t, err, tc.kind, tc.want)
		})
	}
}

// TestDelegateNewPrincipalIDError refuses before any lease when the child principal id cannot be built.
func TestDelegateNewPrincipalIDError(t *testing.T) {
	_, good, svc, f, sh := setup(t)
	boom := errors.New("principal id boom")
	svc.newPrincipalID = func(string) (string, error) { return "", boom }
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "pid", Parent: good})
	failIf(t, resp != nil || !errors.Is(err, boom) || len(f.refs) != 0 || len(sh.reqs) != 0, "Delegate = %+v, %v, leases %d, spawns %d; want the principal id error before any lease", resp, err, len(f.refs), len(sh.reqs))
}

// TestSpawnRefusesEmptyChildPrincipal refuses ErrSpawnBinding before any insert or Spawn.
func TestSpawnRefusesEmptyChildPrincipal(t *testing.T) {
	_, _, svc, f, sh := setup(t)
	err := svc.spawn(context.Background(), &Request{}, envelope.Signed{}, capacity.Lease{ID: "l-1", LaneID: "agent:drv-1"}, "readonly")
	failIf(t, !errors.Is(err, ErrSpawnBinding) || strings.Join(f.calls, ",") != "abort" || len(sh.reqs) != 0, "spawn = %v, calls %v, spawns %d; want ErrSpawnBinding, one abort, no Spawn", err, f.calls, len(sh.reqs))
}

// TestDelegateBindSpawnFailureReleasesHandle aborts and releases the handle when the store refuses the spawn bind.
func TestDelegateBindSpawnFailureReleasesHandle(t *testing.T) {
	c, good, _, _, _ := setup(t)
	f, sh := &fleetDouble{}, &preBindShim{st: c.st}
	svc := NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s,
		Minter: principal.NewMinter(fakeClock{now: t0}), Shim: sh, fleet: f})
	svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "bind-fail", Parent: good})
	failIf(t, resp != nil || err == nil || !sh.released || strings.Join(f.calls, ",") != "abort", "Delegate = %+v, %v, released %v, calls %v; want abort and a released handle", resp, err, sh.released, f.calls)
	wantRefused(t, c, "bind-fail", err)
}

// TestChildRecordDigestErrors names the rule when an envelope has no canonical form.
func TestChildRecordDigestErrors(t *testing.T) {
	_, _, svc, _, _ := setup(t)
	_, err := svc.mintSession(context.Background(), &Request{}, envelope.Envelope{}, "job-1")
	wantCascadeErr(t, err, cascade.KindInvalidInput, "delegation: child")
	_, err = svc.signChild(context.Background(), envelope.Envelope{}, "agent-x")
	wantCascadeErr(t, err, cascade.KindInvalidInput, "delegation: parent")
}
