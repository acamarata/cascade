package delegation

// Purpose: Service.Delegate tests: defaults, parent verification and RepoID before any
// Fleet.Lease, and the spawn leg. Every refusal runs beside a succeeding control.
// SPORT: delegation.service (ADD, P1-AGT-08).

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

type fakeClock struct{ now time.Time } // a runtime.Clock
func (f fakeClock) Now() time.Time     { return f.now }

// fleetDouble grants lease "lease-<n>" (n-th Lease call; "" when noID) on lane (default "agent:drv-1") and logs calls.
type fleetDouble struct {
	lane  string
	noID  bool
	calls []string
	refs  []capacity.DispatchRef // every DispatchRef, one per Lease call
}

func (f *fleetDouble) Lease(_ context.Context, _ capacity.ResourceRequest, d capacity.DispatchRef, _ int) (capacity.Lease, error) {
	f.refs = append(f.refs, d)
	id := map[bool]string{false: fmt.Sprint("lease-", len(f.refs))}[f.noID]
	return capacity.Lease{ID: id, LaneID: conductor.LaneID(cmp.Or(f.lane, "agent:drv-1"))}, nil
}

func (f *fleetDouble) Release(context.Context, capacity.Lease) error { return f.log("release") }
func (f *fleetDouble) Abort(context.Context, capacity.Lease) error   { return f.log("abort") }
func (f *fleetDouble) Commit(context.Context, capacity.Lease) error  { return f.log("commit") }
func (f *fleetDouble) log(call string) error                         { f.calls = append(f.calls, call); return nil }

type shimDouble struct{ reqs []agents.SpawnRequest } // records every Service.shim Spawn

func (d *shimDouble) Spawn(_ context.Context, r agents.SpawnRequest) (agents.SpawnHandle, error) {
	d.reqs = append(d.reqs, r)
	return agents.SpawnHandle{JobID: r.Job.JobID, Release: func(context.Context) error { return nil }}, nil
}

// service returns a Service with fresh doubles and the entitled drv-1.
func (c *chain) service(now time.Time) (*Service, *fleetDouble, *shimDouble) {
	f, sh := &fleetDouble{}, &shimDouble{}
	svc := NewService(Deps{Clock: fakeClock{now: now}, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s,
		Minter: principal.NewMinter(fakeClock{now: now}), Shim: sh, fleet: f})
	svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
	return svc, f, sh
}

// setup returns a chain, a stored first-hop parent record and c.service(t0).
func setup(t *testing.T) (*chain, jobs.DelegationRecord, *Service, *fleetDouble, *shimDouble) {
	c := newChain(t)
	_, good := c.add(c.root)
	svc, f, sh := c.service(t0)
	return c, good, svc, f, sh
}

func TestDelegateAppliesRequestDefaults(t *testing.T) {
	_, rec, svc, f, _ := setup(t)
	req := &Request{AgentID: "agent-def", Parent: rec}
	resp, err := svc.Delegate(context.Background(), req)
	_, nilErr := svc.Delegate(context.Background(), nil)
	failIf(t, err != nil || resp == nil || !resp.Accepted || resp.LeaseID != "lease-1" || len(f.refs) != 1 || req.Deadline == "" || req.Need == "" || req.MinQuality == "" || nilErr == nil, "Delegate = %+v, %v with %d leases, req %+v, Delegate(nil) %v; want an accepted lease and defaults", resp, err, len(f.refs), req, nilErr)
}

// wantCascadeErr asserts a *cascade.Error's Kind and message (errors.Is compares Kind only).
func wantCascadeErr(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	k, ok := cascade.KindOf(err)
	failIf(t, !errors.As(err, new(*cascade.Error)) || !ok || k != kind || !strings.Contains(err.Error(), want), "err = %v, want a *cascade.Error of Kind %s naming %q", err, kind, want)
}

// failIf fails the test now with format and args when cond holds.
func failIf(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if cond {
		t.Fatalf(format, args...)
	}
}

// wantRefused asserts agent's stored record is terminal refused with cause's message.
func wantRefused(t *testing.T, c *chain, agent string, cause error) {
	t.Helper()
	rec, ok, err := c.st.DelegationByAgentPrincipal(context.Background(), agent)
	failIf(t, err != nil || !ok || rec.State != jobs.DelegationRefused || rec.Reason != cause.Error(), "record %s = %+v, %v, %v; want refused with %q", agent, rec, ok, err, cause.Error())
}

// widerRecord stores a child of p whose roots are wider than p's.
func widerRecord(t *testing.T, c *chain, p envelope.Signed) jobs.DelegationRecord {
	t.Helper()
	pd, _ := envelope.Digest(p.Env)
	e := p.Env
	e.ID, e.Principal, e.ParentDigest, e.Roots = "wide", "agent-wide", pd, []string{"/elsewhere"}
	wide, err := envelope.Issue(context.Background(), c.s, e)
	rec, err2 := c.insert(c.wire(wide), wide.Env, nil)
	failIf(t, err != nil || err2 != nil, "%v, %v", err, err2)
	return rec
}

func TestDelegateRefusesBeforeLease(t *testing.T) {
	c := newChain(t)
	p, good := c.add(c.root)
	unsigned := good
	unsigned.Signed = c.wire(envelope.Signed{Env: p.Env, Sig: []byte{}})
	r2, err := MintRoot(context.Background(), c.s, principal.Principal{ID: "p-2"}, srcMut())
	tampered, err2 := c.insert(tamper(t, c.wire(r2)), r2.Env, nil)
	failIf(t, err != nil || err2 != nil, "%v, %v", err, err2)
	cases := []struct {
		name, want string
		parent     jobs.DelegationRecord
		now        time.Time
		kind       cascade.Kind
	}{
		{"missing", "carries no signed parent record", jobs.DelegationRecord{}, t0, cascade.KindIntegrity},
		{"unsigned", "differs from its stored row", unsigned, t0, cascade.KindIntegrity},
		{"tampered", "signature does not verify", tampered, t0, cascade.KindIntegrity},
		{"expired", "expired", good, t0.Add(time.Hour), cascade.KindPermissionDenied},
		{"wider", "delegation: narrow wide", widerRecord(t, c, p), t0, cascade.KindPermissionDenied},
	}
	for _, tc := range cases {
		svc, control, _ := c.service(t0)
		_, cerr := svc.Delegate(context.Background(), &Request{AgentID: "a-" + tc.name, Parent: good})
		svc, f, _ := c.service(tc.now)
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: tc.parent})
		failIf(t, cerr != nil || len(control.refs) != 1 || resp != nil || len(f.refs) != 0, "%s: control %v with %d leases; Delegate returned %+v with %d Lease calls; want one, then none", tc.name, cerr, len(control.refs), resp, len(f.refs))
		wantCascadeErr(t, err, tc.kind, tc.want)
	}
	_, err = NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s}).Delegate(context.Background(), &Request{Parent: good})
	wantCascadeErr(t, err, cascade.KindInvalidInput, "a fleet is required")
	_, err = NewService(Deps{Store: c.st}).Delegate(context.Background(), &Request{Parent: good})
	wantCascadeErr(t, err, cascade.KindInvalidInput, "pinned controller key are required")
}

func TestDelegateMutationRequiresJobID(t *testing.T) {
	_, good, svc, f, _ := setup(t)
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Mutation: true, InScope: []string{"cmd"}, Parent: good})
	failIf(t, resp != nil || err != ErrInvalidParam || len(f.refs) != 0, "Delegate = %+v, %v with %d leases; want ErrInvalidParam and no lease", resp, err, len(f.refs))
	wantCascadeErr(t, err, cascade.KindInvalidInput, "job_id is required for a mutation")
	for i, tc := range []struct{ job, policy, prefixes string }{{"", "readonly", ""}, {"j1", "readonly", "cmd"}, {"j1", "all", "cmd"}} {
		resp, err = svc.Delegate(context.Background(), &Request{AgentID: fmt.Sprint("a", i+1), JobID: tc.job, Mutation: i == 2, InScope: []string{"cmd"}, Parent: good})
		failIf(t, err != nil || resp == nil || resp.ToolPolicy != tc.policy || strings.Join(resp.InScopePrefixes, ",") != tc.prefixes || len(f.refs) != i+1, "case %d: Delegate = %+v, %v (%d leases); want %s with prefixes %q", i, resp, err, len(f.refs), tc.policy, tc.prefixes)
	}
}

func TestDelegateRefusesJobOutsideCallerScope(t *testing.T) {
	_, good, svc, f, _ := setup(t)
	req := func() *Request {
		return &Request{AgentID: fmt.Sprint("a", len(f.refs)), JobID: "j1", JobProject: "proj-a", Mutation: true, InScope: []string{"cmd"}, Parent: good}
	}
	other := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-x", Scope: "proj-b"}})
	resp, err := svc.Delegate(other, req())
	failIf(t, resp != nil || len(f.refs) != 0, "outside scope: Delegate = %+v with %d leases; want a refusal and no lease", resp, len(f.refs))
	wantCascadeErr(t, err, cascade.KindPermissionDenied, `job j1 is outside the caller scope "proj-b"`)
	same := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-y", Scope: "proj-a"}})
	resp, err = svc.Delegate(same, req())
	failIf(t, err != nil || resp == nil || len(f.refs) != 1, "same scope: Delegate = %+v, %v with %d leases; want one lease", resp, err, len(f.refs))
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	resp, err = svc.Delegate(owner, req())
	failIf(t, err != nil || resp == nil || resp.ToolPolicy != "all" || len(f.refs) != 2, "owner CLI: Delegate = %+v, %v with %d leases; want a mutation lease", resp, err, len(f.refs))
}

func TestDelegateResolvesDriverForLeasedAgentLane(t *testing.T) {
	_, good, svc, f, _ := setup(t)
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good})
	failIf(t, err != nil || resp == nil || resp.Driver != "drv-1" || len(f.refs) != 1 || strings.Join(f.calls, ",") != "commit", "Delegate = %+v, %v (%d leases, calls %v); want driver drv-1", resp, err, len(f.refs), f.calls)
	f.lane = "agent:unknown"
	resp, err = svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good})
	failIf(t, resp != nil || len(f.refs) != 2 || strings.Join(f.calls, ",") != "commit,release", "unresolvable: Delegate = %+v (%d leases, calls %v); want a released lease", resp, len(f.refs), f.calls)
	wantCascadeErr(t, err, cascade.KindUnavailable, "agent:unknown")
}

// nodeSigner is a node-role installation signer: it refuses every Sign.
type nodeSigner struct{ testSigner }

func (nodeSigner) Sign(context.Context, []byte) ([]byte, error) {
	return nil, cascade.New(cascade.KindPermissionDenied, "installation key: only the controller daemon holds the installation identity key")
}

func TestDelegateSignsWithInstallationKey(t *testing.T) {
	c, good, svc, _, _ := setup(t)
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "agent-7", Parent: good})
	failIf(t, err != nil || resp == nil, "Delegate = %+v, %v", resp, err)
	env, err := envelope.Verify(resp.Record, c.s.PublicKey(), t0) // the installation key
	_, ferr := envelope.Verify(resp.Record, newSigner(t).PublicKey(), t0)
	failIf(t, err != nil || env.Principal != "agent-7" || ferr == nil, "record: Verify = %+v, %v (foreign key: %v); want principal agent-7 under the installation key only", env, err, ferr)
	f := &fleetDouble{}
	resp, err = NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey(), Signer: nodeSigner{c.s}, fleet: f}).Delegate(context.Background(), &Request{AgentID: "agent-7", Parent: good})
	failIf(t, resp != nil || len(f.refs) != 0, "node role: Delegate = %+v with %d leases; want a refusal and no lease", resp, len(f.refs))
	wantCascadeErr(t, err, cascade.KindPermissionDenied, "only the controller daemon")
}

func TestDelegateEntitlementReleasesLeaseFirst(t *testing.T) {
	_, good, svc, f, sh := setup(t)
	svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1"}})
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "noent", Parent: good})
	failIf(t, resp != nil || err != provider.ErrEntitlement || strings.Join(f.calls, ",") != "abort" || len(sh.reqs) != 0, "Delegate = %+v, %v, calls %v, %d spawns; want Abort then ErrEntitlement", resp, err, f.calls, len(sh.reqs))
	wantCascadeErr(t, err, cascade.KindPolicyDenied, "no programmatic entitlement")
}

func TestDelegatePreSpawnGateRefusalAborts(t *testing.T) {
	isolateGates(t)
	c, good, svc, f, sh := setup(t)
	refuse := cascade.New(cascade.KindPolicyDenied, "budget: debit refused")
	RegisterPreSpawnGate("budget", func(_ context.Context, rec jobs.DelegationRecord, _ envelope.Envelope) error {
		return map[bool]error{false: refuse}[rec.LeaseMarker != "" && rec.AgentPrincipal == "pass"] // the marker is bound before every gate
	})
	_, err := svc.Delegate(context.Background(), &Request{AgentID: "pass", Parent: good})
	failIf(t, err != nil || len(sh.reqs) != 1, "control: err = %v, %d spawns; want one spawn", err, len(sh.reqs))
	f.calls, sh.reqs = nil, nil
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "gated", Parent: good})
	failIf(t, resp != nil || err != refuse || strings.Join(f.calls, ",") != "abort" || len(sh.reqs) != 0, "Delegate = %+v, %v, calls %v, %d spawns; want the gate's refusal and an Abort", resp, err, f.calls, len(sh.reqs))
	wantCascadeErr(t, err, cascade.KindPolicyDenied, "budget: debit refused")
	wantRefused(t, c, "gated", refuse)
}

// boundSpawn asserts r is resp's spawn, bound to its record with lease lid and the minted
// child principal (SessionID = provider.SessionIDFor(JobID)); it returns that principal.
func boundSpawn(t *testing.T, c *chain, svc *Service, resp *Response, r agents.SpawnRequest, lid string) principal.Principal {
	t.Helper()
	id := resp.Record.Env.ID
	rec, err := c.st.GetDelegation(context.Background(), id)
	p, perr := svc.deps.Minter.Resolve(r.PrincipalToken, t0)
	failIf(t, err != nil || perr != nil || rec.AgentJobID != string(r.Job.JobID) || rec.LeaseMarker != r.LeaseMarker || string(r.Job.JobID) != id || p.SessionID != provider.SessionIDFor(r.Job.JobID) || p.SessionID == id ||
		r.LeaseID == "" || r.LeaseID != lid || rec.LeaseID != lid || r.ChildPrincipal == "" || r.ChildPrincipal != p.ID,
		"spawn %+v, record %+v (%v), principal %+v (%v); want lease %q and the minted child bound", r, rec, err, p, perr, lid)
	return p
}

func TestDelegateMintsSpawnSessionID(t *testing.T) {
	c, good, svc, f, sh := setup(t)
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "sess", Parent: good})
	failIf(t, err != nil || len(sh.reqs) != 1 || strings.Join(f.calls, ",") != "commit", "Delegate = %+v, %v, %d spawns, calls %v; want one spawn and a commit", resp, err, len(sh.reqs), f.calls)
	p := boundSpawn(t, c, svc, resp, sh.reqs[0], "lease-1")
	failIf(t, p.ID != "sess", "principal = %+v; want the child sess", p)
}

func TestDelegatePassesDispatchRepoID(t *testing.T) {
	c, good, _, _, _ := setup(t)
	roots, down := []string(nil), cascade.New(cascade.KindUnavailable, "repos: registry unavailable")
	answer := func(id string, err error) func(context.Context, string) (string, error) {
		return func(_ context.Context, root string) (string, error) { roots = append(roots, root); return id, err }
	}
	run := func(agent string, mutation bool, seam func(context.Context, string) (string, error)) (*Response, *fleetDouble, error) {
		svc, f, _ := c.service(t0)
		svc.deps.RepoIDOf, roots = seam, nil
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: agent, JobID: "j1", Mutation: mutation, RepoRoot: "/wt/repo", Parent: good})
		return resp, f, err
	}
	check := func(name string, mutation bool, seam func(context.Context, string) (string, error), want string, calls int) {
		_, f, err := run(name, mutation, seam)
		failIf(t, err != nil || len(f.refs) != 1 || f.refs[0].RepoID != want || len(roots) != calls || (calls == 1 && roots[0] != "/wt/repo"), "%s: err = %v, refs %+v, RepoIDOf roots %q; want RepoID %q after %d calls", name, err, f.refs, roots, want, calls)
	}
	check("mutation", true, answer("repo-a", nil), "repo-a", 1)                                                // (a)
	check("reason-only", false, answer("repo-a", nil), "", 0)                                                  // (b)
	check("unregistered", true, answer("", cascade.New(cascade.KindNotFound, "repos: not registered")), "", 1) // (c)
	check("nil-seam", true, nil, "", 0)                                                                        // (c)
	resp, f, err := run("down", true, answer("repo-a", down))                                                  // (d)
	failIf(t, resp != nil || err != down || len(f.refs) != 0, "unavailable: Delegate = %+v, %v with %d Lease calls; want the resolver error and none", resp, err, len(f.refs))
	wantCascadeErr(t, err, cascade.KindUnavailable, "repos: registry unavailable")
	wantRefused(t, c, "down", down)
}

func TestDelegateSpawnCarriesLeaseAndChildPrincipal(t *testing.T) {
	c, good, svc, _, sh := setup(t)
	caller := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-caller", Scope: "proj-a"}})
	for i, mutation := range []bool{true, false} { // (a) one SpawnRequest per delegation
		resp, err := svc.Delegate(caller, &Request{AgentID: fmt.Sprint("child-", i), JobID: "j1", JobProject: "proj-a", Mutation: mutation, Parent: good})
		failIf(t, err != nil || len(sh.reqs) != i+1, "mutation %v: err = %v, %d spawns; want exactly one spawn per delegation", mutation, err, len(sh.reqs))
		p := boundSpawn(t, c, svc, resp, sh.reqs[i], fmt.Sprint("lease-", i+1))
		failIf(t, p.ID == "p-caller", "mutation %v: the child principal is the caller's %q", mutation, p.ID)
	}
	live, lerr := c.st.LiveDelegations(context.Background())
	for _, agent := range []string{"no-lease", "no-child"} { // (b) an empty Lease ID, (c) an empty child principal
		svc, f, sh := c.service(t0)
		f.noID = agent == "no-lease"
		if agent == "no-child" {
			svc.newPrincipalID = func(string) (string, error) { return "", nil }
		}
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: agent, Parent: good})
		failIf(t, resp != nil || err != ErrSpawnBinding || strings.Join(f.calls, ",") != "abort" || len(sh.reqs) != 0, "%s: Delegate = %+v, %v, calls %v, %d spawns; want ErrSpawnBinding, one Abort, no spawn", agent, resp, err, f.calls, len(sh.reqs))
		wantCascadeErr(t, err, cascade.KindInternal, "delegation: the spawn carries no lease id or child principal")
	}
	wantRefused(t, c, "no-lease", ErrSpawnBinding)
	after, err := c.st.LiveDelegations(context.Background())
	failIf(t, lerr != nil || err != nil || len(after) != len(live), "live records %d -> %d (%v, %v); want no live record left by a refusal", len(live), len(after), lerr, err)
}
