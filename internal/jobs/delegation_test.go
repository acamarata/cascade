package jobs

// Purpose: the delegation record store's API tests (contract:delegation-
//
//	record): set-once bindings, live -> terminal, depth and fan-out
//	derived and enforced inside the insert transaction, and the refusals
//	of caller-supplied fields.
//
// SPORT: jobs/delegation-record/ADD (P1-AGT-11).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

func TestDelegationEnvelopeBindOnce(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	f.add(f.root, "a")
	wantErr(t, f.st.BindDelegationSpawn(ctx, "a", "lease-1", "ajob-1"), cascade.KindConflict, "lease marker is not bound yet")
	if err := f.st.BindDelegationMarker(ctx, "a", "clm_1"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.BindDelegationSpawn(ctx, "a", "lease-1", "ajob-1"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.st.BindDelegationSpawn(ctx, "a", "lease-1", "ajob-1"), cascade.KindConflict, "spawn is already bound")
	wantErr(t, f.st.BindDelegationSpawn(ctx, "a", "lease-2", "ajob-2"), cascade.KindConflict, "spawn is already bound")
	if r := f.get("a"); r.LeaseID != "lease-1" || r.AgentJobID != "ajob-1" {
		t.Fatalf("binding overwritten: %+v", r)
	}
	if err := f.rawExec(`UPDATE jobs_delegation SET lease_id = 'lease-9' WHERE id = 'a'`); err == nil {
		t.Fatal("raw re-bind accepted")
	}
	wantErr(t, f.st.BindDelegationSpawn(ctx, "a", "", "ajob-1"), cascade.KindInvalidInput, "lease ID and an agent job ID")
	wantErr(t, f.st.BindDelegationSpawn(ctx, "nope", "l", "j"), cascade.KindNotFound, `"nope" does not exist`)
	if err := f.st.FinishDelegation(ctx, "a", DelegationFailed, "x"); err != nil {
		t.Fatal(err)
	}
	f.add(f.root, "b")
	_ = f.st.FinishDelegation(ctx, "b", DelegationCancelled, "")
	wantErr(t, f.st.BindDelegationMarker(ctx, "b", "clm_2"), cascade.KindConflict, "it is cancelled")
}

func TestBindDelegationMarkerSetOnce(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	f.add(f.root, "a")
	if err := f.st.BindDelegationMarker(ctx, "a", "clm_1"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.st.BindDelegationMarker(ctx, "a", "clm_1"), cascade.KindConflict, "lease marker is already bound")
	wantErr(t, f.st.BindDelegationMarker(ctx, "a", "clm_2"), cascade.KindConflict, "lease marker is already bound")
	if r := f.get("a"); r.LeaseMarker != "clm_1" {
		t.Fatalf("marker = %q, want clm_1", r.LeaseMarker)
	}
	err := f.rawExec(`UPDATE jobs_delegation SET lease_marker = 'clm_3' WHERE id = 'a'`)
	wantRaw(t, err, msgDelegationMarkOnce)
	wantErr(t, f.st.BindDelegationMarker(ctx, "a", ""), cascade.KindInvalidInput, "lease marker is required")
	wantErr(t, f.st.BindDelegationMarker(ctx, "nope", "m"), cascade.KindNotFound, "does not exist")
}

func TestDelegationFinishLiveToTerminalOnly(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	f.add(f.root, "a")
	for _, st := range []DelegationState{DelegationLive, "bogus", ""} {
		wantErr(t, f.st.FinishDelegation(ctx, "a", st, ""), cascade.KindInvalidInput, "not a terminal delegation state")
	}
	if err := f.st.FinishDelegation(ctx, "a", DelegationRefused, "budget"); err != nil {
		t.Fatal(err)
	}
	if r := f.get("a"); r.State != DelegationRefused || r.Reason != "budget" {
		t.Fatalf("record = %+v", r)
	}
	for _, st := range []DelegationState{DelegationSucceeded, DelegationRefused} {
		wantErr(t, f.st.FinishDelegation(ctx, "a", st, "again"), cascade.KindConflict, "it is refused")
	}
	wantRaw(t, f.rawExec(`UPDATE jobs_delegation SET state = 'live' WHERE id = 'a'`), msgDelegationTerminal)
	wantRaw(t, f.rawExec(`UPDATE jobs_delegation SET reason = 'x' WHERE id = 'a'`), msgDelegationTerminal)
	wantErr(t, f.st.FinishDelegation(ctx, "nope", DelegationFailed, ""), cascade.KindNotFound, "does not exist")
	f.add(f.root, "b")
	wantRaw(t, f.rawExec(`UPDATE jobs_delegation SET reason = 'x' WHERE id = 'b'`), msgDelegationState)
}

func TestFanOutCapEnforcedInInsertTx(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	p := f.add(f.root, "p")
	for i := 0; i < DelegationMaxLiveChildren-1; i++ {
		f.add(p, fmt.Sprintf("c%d", i))
	}
	recs := make([]DelegationRecord, 2)
	recs[0], _ = f.childOf(p, "x0")
	recs[1], _ = f.childOf(p, "x1")
	errs := make([]error, 2)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range recs {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			errs[i] = f.st.InsertDelegation(ctx, recs[i])
		}(i)
	}
	start.Done()
	done.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("errs = %v, want exactly one insert to succeed", errs)
	}
	for _, err := range errs {
		if err != nil {
			wantErr(t, err, cascade.KindPermissionDenied, "fan-out")
		}
	}
	pd := f.get("p").EnvelopeDigest
	if n, err := f.st.CountLiveChildren(ctx, pd); err != nil || n != DelegationMaxLiveChildren {
		t.Fatalf("CountLiveChildren = %d, %v; want %d", n, err, DelegationMaxLiveChildren)
	}
	if err := f.st.FinishDelegation(ctx, "c0", DelegationSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f.add(p, "y")
}

func TestDepthFromStoredParent(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	r, env1 := f.childOf(f.root, "d1")
	r.ParentSigned, r.Depth, r.OriginPrincipal = f.rootSigned, 99, "forged"
	if err := f.st.InsertDelegation(ctx, r); err != nil {
		t.Fatal(err)
	}
	env2 := f.add(env1, "d2")
	env3 := f.add(env2, "d3")
	for id, want := range map[string]int{"d1": 1, "d2": 2, "d3": 3} {
		if g := f.get(id); g.Depth != want || g.OriginPrincipal != "owner-cli" {
			t.Fatalf("%s: depth %d origin %q, want %d owner-cli", id, g.Depth, g.OriginPrincipal, want)
		}
	}
	r4, _ := f.childOf(env3, "d4")
	wantErr(t, f.st.InsertDelegation(ctx, r4), cascade.KindPermissionDenied, "depth 4 exceeds MaxDepth 3")
	b, root := f.sign(dlgEnv("root2", "agent-root2", ""))
	rr := DelegationRecord{ID: "root2", Signed: b, AgentPrincipal: "agent-root2", State: DelegationLive, CreatedAt: dlgT0, Depth: 7}
	if err := f.st.InsertDelegation(ctx, rr); err != nil {
		t.Fatal(err)
	}
	if g := f.get("root2"); g.Depth != 0 || g.OriginPrincipal != "agent-root2" || len(g.ParentSigned) != 0 {
		t.Fatalf("root record = %+v", g)
	}
	f.add(root, "r1")
	if g := f.get("r1"); g.Depth != 1 || g.OriginPrincipal != "agent-root2" {
		t.Fatalf("child of a stored root = %+v", g)
	}
}

func TestDelegationInsertRefusals(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	a := f.add(f.root, "a")
	base, _ := f.childOf(a, "b")
	cases := []struct {
		mut  func(*DelegationRecord)
		kind cascade.Kind
		want string
	}{
		{func(r *DelegationRecord) { r.State = DelegationSucceeded }, cascade.KindInvalidInput, "inserted live"},
		{func(r *DelegationRecord) { r.LeaseMarker = "m" }, cascade.KindInvalidInput, "set after insert"},
		{func(r *DelegationRecord) { r.CreatedAt = time.Time{} }, cascade.KindInvalidInput, "CreatedAt"},
		{func(r *DelegationRecord) { r.Signed = []byte("{}") }, cascade.KindInvalidInput, "envelope"},
		{func(r *DelegationRecord) { r.ID = "other" }, cascade.KindInvalidInput, "is not the envelope ID"},
		{func(r *DelegationRecord) { r.AgentPrincipal = "agent-x" }, cascade.KindInvalidInput, "is not the envelope principal"},
		{func(r *DelegationRecord) { r.ParentSigned = f.rootSigned }, cascade.KindIntegrity, "not the stored parent's Signed"},
	}
	for _, c := range cases {
		r := base
		c.mut(&r)
		wantErr(t, f.st.InsertDelegation(ctx, r), c.kind, c.want)
	}
	if err := f.st.InsertDelegation(ctx, base); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.st.InsertDelegation(ctx, base), cascade.KindConflict, "already recorded")
	if err := f.st.FinishDelegation(ctx, "a", DelegationFailed, ""); err != nil {
		t.Fatal(err)
	}
	late, _ := f.childOf(a, "late")
	wantErr(t, f.st.InsertDelegation(ctx, late), cascade.KindConflict, "is failed, not live")
}

func TestDelegationFirstHopRefusals(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	r, _ := f.childOf(f.root, "a")
	wantErr(t, f.st.InsertDelegation(ctx, r), cascade.KindNotFound, "unknown parent envelope")
	r.ParentSigned = []byte("{")
	wantErr(t, f.st.InsertDelegation(ctx, r), cascade.KindIntegrity, "ParentSigned does not decode")
	other, _ := f.sign(dlgEnv("root-b", "owner-cli", ""))
	r.ParentSigned = other
	wantErr(t, f.st.InsertDelegation(ctx, r), cascade.KindIntegrity, "does not hash to ParentDigest")
	midB, mid := f.sign(dlgEnv("mid", "agent-mid", "feed"))
	c, _ := f.childOf(mid, "c")
	c.ParentSigned = midB
	wantErr(t, f.st.InsertDelegation(ctx, c), cascade.KindNotFound, "neither a stored record nor a root")
	same, _ := f.childOf(f.root, "root")
	same.ParentSigned = f.rootSigned
	wantErr(t, f.st.InsertDelegation(ctx, same), cascade.KindIntegrity, "cycle")
	rootRec := DelegationRecord{ID: "root", Signed: f.rootSigned, AgentPrincipal: "owner-cli",
		State: DelegationLive, CreatedAt: dlgT0, ParentSigned: f.rootSigned}
	wantErr(t, f.st.InsertDelegation(ctx, rootRec), cascade.KindInvalidInput, "root record carries no ParentSigned")
	f.add(f.root, "a")
	rd, _ := envelope.Digest(f.root)
	b2, _ := f.sign(dlgEnv("a2", "agent-a", rd))
	dup := DelegationRecord{ID: "a2", Signed: b2, ParentSigned: f.rootSigned, AgentPrincipal: "agent-a",
		State: DelegationLive, CreatedAt: dlgT0}
	wantErr(t, f.st.InsertDelegation(ctx, dup), cascade.KindConflict, "already recorded")
}

func TestLiveDelegationsListsOnlyLive(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		f.add(f.root, id)
	}
	if err := f.st.FinishDelegation(ctx, "b", DelegationSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	live, err := f.st.LiveDelegations(ctx)
	if err != nil || len(live) != 2 || live[0].ID != "a" || live[1].ID != "c" {
		t.Fatalf("LiveDelegations = %+v, %v; want [a c]", live, err)
	}
	for _, r := range live {
		if r.State != DelegationLive || r.Depth != 1 || !r.CreatedAt.Equal(dlgT0) || !r.UpdatedAt.Equal(dlgT0) {
			t.Fatalf("live record = %+v", r)
		}
	}
	got, ok, err := f.st.DelegationByAgentPrincipal(ctx, "agent-b")
	if err != nil || !ok || got.ID != "b" || got.State != DelegationSucceeded {
		t.Fatalf("DelegationByAgentPrincipal(agent-b) = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := f.st.DelegationByAgentPrincipal(ctx, "agent-zz"); ok || err != nil {
		t.Fatalf("unknown principal: ok %v err %v", ok, err)
	}
	_, _, err = f.st.DelegationByAgentPrincipal(ctx, "")
	wantErr(t, err, cascade.KindInvalidInput, "principal ID is required")
	_, err = f.st.CountLiveChildren(ctx, "")
	wantErr(t, err, cascade.KindInvalidInput, "parent digest is required")
	_, err = f.st.GetDelegation(ctx, "nope")
	wantErr(t, err, cascade.KindNotFound, `"nope" does not exist`)
}
