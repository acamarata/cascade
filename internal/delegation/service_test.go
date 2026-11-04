package delegation

// Purpose: Service.Delegate tests: request defaults, and the parent
//
//	record verification that must refuse before any Fleet.Lease call.
//	Every refusal case runs beside a control that leases, so a Delegate
//	that refuses everything fails.
//
// SPORT: delegation.service (ADD, P1-AGT-08).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// fakeClock implements runtime.Clock for tests.
type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

// fleetDouble records every Lease call and grants a fixed lease.
type fleetDouble struct{ leases int }

func (f *fleetDouble) Lease(context.Context, capacity.ResourceRequest, capacity.DispatchRef, int) (capacity.Lease, error) {
	f.leases++
	return capacity.Lease{ID: "lease-1"}, nil
}

// service returns a Service over c's store with a fresh fleet double.
func (c *chain) service(now time.Time) (*Service, *fleetDouble) {
	f := &fleetDouble{}
	return NewService(Deps{Clock: fakeClock{now: now}, Store: c.st, Pinned: c.s.PublicKey(), fleet: f}), f
}

// TestDelegateAppliesRequestDefaults verifies default values are populated.
func TestDelegateAppliesRequestDefaults(t *testing.T) {
	c := newChain(t)
	_, rec := c.add(c.root)
	svc, f := c.service(t0)
	req := &Request{AgentID: "agent-1", Parent: rec}
	resp, err := svc.Delegate(context.Background(), req)
	if err != nil || resp == nil || !resp.Accepted || resp.LeaseID != "lease-1" || f.leases != 1 {
		t.Fatalf("Delegate = %+v, %v with %d leases; want an accepted lease", resp, err, f.leases)
	}
	if req.Deadline == "" || req.Need == "" || req.MinQuality == "" {
		t.Errorf("defaults not applied: %+v", req)
	}
	if _, err := svc.Delegate(context.Background(), nil); err == nil {
		t.Fatal("Delegate(nil) succeeded")
	}
}

// wantCascadeErr asserts err is a *cascade.Error of kind whose message
// names want; errors.Is alone compares Kind only.
func wantCascadeErr(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce == nil {
		t.Fatalf("err = %v, want a *cascade.Error", err)
	}
	if k, ok := cascade.KindOf(err); !ok || k != kind || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want Kind %s naming %q", err, kind, want)
	}
}

// widerRecord stores a child of p whose roots are wider than p's.
func widerRecord(t *testing.T, c *chain, p envelope.Signed) jobs.DelegationRecord {
	t.Helper()
	pd, _ := envelope.Digest(p.Env)
	e := p.Env
	e.ID, e.Principal, e.ParentDigest, e.Roots = "wide", "agent-wide", pd, []string{"/elsewhere"}
	wide, err := envelope.Issue(context.Background(), c.s, e)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := c.insert(c.wire(wide), wide.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestDelegateRefusesBeforeLease(t *testing.T) {
	c := newChain(t)
	p, good := c.add(c.root)
	unsigned := good
	unsigned.Signed = c.wire(envelope.Signed{Env: p.Env, Sig: []byte{}})
	r2, err := MintRoot(context.Background(), c.s, principal.Principal{ID: "p-2"}, srcMut())
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := c.insert(tamper(t, c.wire(r2)), r2.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		parent jobs.DelegationRecord
		now    time.Time
		kind   cascade.Kind
		want   string
	}{
		{"missing", jobs.DelegationRecord{}, t0, cascade.KindIntegrity, "carries no signed parent record"},
		{"unsigned", unsigned, t0, cascade.KindIntegrity, "differs from its stored row"},
		{"tampered", tampered, t0, cascade.KindIntegrity, "signature does not verify"},
		{"expired", good, t0.Add(time.Hour), cascade.KindPermissionDenied, "expired"},
		{"wider", widerRecord(t, c, p), t0, cascade.KindPermissionDenied, "delegation: narrow wide"},
	}
	for _, tc := range cases {
		svc, f := c.service(t0)
		if _, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: good}); err != nil || f.leases != 1 {
			t.Fatalf("%s control: err = %v, leases = %d; want one lease", tc.name, err, f.leases)
		}
		svc, f = c.service(tc.now)
		resp, err := svc.Delegate(context.Background(), &Request{AgentID: "a", Parent: tc.parent})
		if resp != nil {
			t.Fatalf("%s: Delegate returned %+v", tc.name, resp)
		}
		wantCascadeErr(t, err, tc.kind, tc.want)
		if f.leases != 0 {
			t.Fatalf("%s: the fleet double recorded %d Lease calls, want 0", tc.name, f.leases)
		}
	}
	noFleet := NewService(Deps{Clock: fakeClock{now: t0}, Store: c.st, Pinned: c.s.PublicKey()})
	_, err = noFleet.Delegate(context.Background(), &Request{Parent: good})
	wantCascadeErr(t, err, cascade.KindInvalidInput, "a fleet is required")
	_, err = NewService(Deps{Store: c.st}).Delegate(context.Background(), &Request{Parent: good})
	wantCascadeErr(t, err, cascade.KindInvalidInput, "pinned controller key are required")
}

func TestDelegateMutationRequiresJobID(t *testing.T) {
	c := newChain(t)
	_, good := c.add(c.root)
	svc, f := c.service(t0)
	ctx := context.Background()
	resp, err := svc.Delegate(ctx, &Request{AgentID: "a", Mutation: true, InScope: []string{"cmd"}, Parent: good})
	if resp != nil || err != ErrInvalidParam || f.leases != 0 {
		t.Fatalf("Delegate = %+v, %v with %d leases; want ErrInvalidParam and no lease", resp, err, f.leases)
	}
	wantCascadeErr(t, err, cascade.KindInvalidInput, "job_id is required for a mutation")
	resp, err = svc.Delegate(ctx, &Request{AgentID: "a", InScope: []string{"cmd"}, Parent: good})
	if err != nil || resp == nil || len(resp.InScopePrefixes) != 0 || resp.ToolPolicy != "readonly" {
		t.Fatalf("no job_id: Delegate = %+v, %v; want readonly with no prefixes", resp, err)
	}
	resp, err = svc.Delegate(ctx, &Request{AgentID: "a", JobID: "j1", InScope: []string{"cmd"}, Parent: good})
	if err != nil || resp == nil || resp.ToolPolicy != "readonly" || strings.Join(resp.InScopePrefixes, ",") != "cmd" {
		t.Fatalf("read-only job: Delegate = %+v, %v; want readonly with the job prefixes", resp, err)
	}
	resp, err = svc.Delegate(ctx, &Request{AgentID: "a", JobID: "j1", Mutation: true, InScope: []string{"cmd"}, Parent: good})
	if err != nil || resp == nil || resp.ToolPolicy != "all" || strings.Join(resp.InScopePrefixes, ",") != "cmd" {
		t.Fatalf("mutation job: Delegate = %+v, %v; want all with the job prefixes", resp, err)
	}
	if f.leases != 3 {
		t.Fatalf("fleet double recorded %d leases, want 3", f.leases)
	}
}

func TestDelegateRefusesJobOutsideCallerScope(t *testing.T) {
	c := newChain(t)
	_, good := c.add(c.root)
	req := func() *Request {
		return &Request{AgentID: "a", JobID: "j1", JobProject: "proj-a", Mutation: true, InScope: []string{"cmd"}, Parent: good}
	}
	other := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-x", Scope: "proj-b"}})
	svc, f := c.service(t0)
	resp, err := svc.Delegate(other, req())
	if resp != nil || f.leases != 0 {
		t.Fatalf("outside scope: Delegate = %+v with %d leases; want a refusal and no lease", resp, f.leases)
	}
	wantCascadeErr(t, err, cascade.KindPermissionDenied, `job j1 is outside the caller scope "proj-b"`)
	same := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-y", Scope: "proj-a"}})
	if resp, err = svc.Delegate(same, req()); err != nil || resp == nil || f.leases != 1 {
		t.Fatalf("same scope: Delegate = %+v, %v with %d leases; want one lease", resp, err, f.leases)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if resp, err = svc.Delegate(owner, req()); err != nil || resp == nil || resp.ToolPolicy != "all" || f.leases != 2 {
		t.Fatalf("owner CLI: Delegate = %+v, %v with %d leases; want a mutation lease", resp, err, f.leases)
	}
}
