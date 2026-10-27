//go:build !windows

// Purpose: the tokenless approval.grant on the daemon records the human
//
//	approval of a queued request and never redeems it: the owner that
//	queued it redeems once with its own nonce, a second grant refuses, a
//	session principal cannot grant, the CLI reports "approved", and a crash
//	between the decision and its confirm leaves an unknown-outcome effect
//	that refuses every later grant.
//
// Inputs: a fresh HOME / CASCADE_HOME, an injected elevation custody, the
//
//	file vault forced by TestMain; the crash leg re-runs this test binary.
//
// SPORT: cmd/cascade held-record decision tests (P1-SEC-41).

package main

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// enqueueL3 queues one L3 approval against a registered boot capability, as
// an owner would; the result carries the nonce only that owner holds.
func (d approvalDaemon) enqueueL3(t *testing.T, action string) policy.EnqueueResult {
	t.Helper()
	res, err := d.Pol.Queue.Enqueue(context.Background(), policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "fake-owner"},
		Capability: githubCICapabilities()[0].Name, Level: policy.L3,
		Action: action, Params: fakeParams(action), Summary: action + " summary",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return res
}

// ownerRedeem is the owner's one ConsumeToken with its own nonce.
func (d approvalDaemon) ownerRedeem(res policy.EnqueueResult, action string) error {
	_, err := d.Pol.Queue.ConsumeToken(context.Background(), policy.ConsumeRequest{
		RequestID: res.RequestID, Nonce: res.Token.Nonce, Action: action, Params: fakeParams(action)})
	return err
}

// fakeParams gives each queued action its own params, so its params hash
// tells its audit rows apart from another request's.
func fakeParams(action string) []byte { return []byte(`{"ref":"` + action + `"}`) }

// showState is approval.show's state for id over the daemon registry.
func (d approvalDaemon) showState(t *testing.T, id string) string {
	t.Helper()
	var out policy.ApprovalShowResult
	if err := d.Call(context.Background(), "approval.show", policy.ApprovalShowParams{RequestID: id}, &out); err != nil {
		t.Fatalf("approval.show %s: %v", id, err)
	}
	return out.State
}

// sessionCall dispatches method as a session principal, signing the
// attestation the daemon asks for, so only the access check can refuse it.
func (d approvalDaemon) sessionCall(method string, params any) error {
	p := principal.Principal{ID: "fake-session-principal", Kind: principal.KindSession, SessionID: "fake-session"}
	ctx := principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	raw, _ := json.Marshal(params)
	_, eo := d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
	if eo == nil {
		return nil
	}
	if nonce := nonceOf(eo); nonce != "" {
		att, err := signAttestation(d.KS, runtime.NewSystemClock(), method, rpc.ParamsHash(raw), nonce)
		if err != nil {
			return err
		}
		env, _ := json.Marshal(map[string]any{"_attestation": att, "_args": json.RawMessage(raw)})
		if _, eo = d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: env}); eo == nil {
			return nil
		}
	}
	return eo
}

func TestDaemonHeldGrantOwnerRedeems(t *testing.T) {
	d := newApprovalDaemon(t, nil)
	ctx := context.Background()
	res := d.enqueueL3(t, "fake merge")
	if st := d.showState(t, res.RequestID); st != "pending" {
		t.Fatalf("queued entry reads %q, want pending", st)
	}
	var dec policy.DecisionResult
	err := d.Call(ctx, "approval.grant", policy.ApprovalGrantParams{RequestID: res.RequestID}, &dec)
	if err != nil || dec != (policy.DecisionResult{RequestID: res.RequestID, State: "approved"}) {
		t.Fatalf("tokenless grant = %+v, %v; want approved", dec, err)
	}
	if st := d.showState(t, res.RequestID); st != "approved" {
		t.Fatalf("after the grant: %q, want approved", st)
	}
	if err := d.ownerRedeem(res, "fake merge"); err != nil {
		t.Fatalf("the owner's ConsumeToken: %v", err)
	}
	if st := d.showState(t, res.RequestID); st != "consumed" {
		t.Fatalf("after the owner redeemed: %q, want consumed", st)
	}
	err = d.Call(ctx, "approval.grant", policy.ApprovalGrantParams{RequestID: res.RequestID}, nil)
	if err == nil || !strings.Contains(err.Error(), "no held approval record") || d.showState(t, res.RequestID) != "consumed" {
		t.Fatalf("grant after redemption = %v, want a refusal and the entry still consumed", err)
	}
	fresh := d.enqueueL3(t, "fake merge two")
	if err := d.sessionCall("approval.grant", policy.ApprovalGrantParams{RequestID: fresh.RequestID}); err == nil ||
		!strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("a session principal's grant = %v, want the access refusal", err)
	}
	if st := d.showState(t, fresh.RequestID); st != "pending" {
		t.Fatalf("after the session principal's grant: %q, want pending", st)
	}
	assertHeldGrantAudit(t, d, res)
}

// assertHeldGrantAudit checks the one audit log: the enqueue, the decision
// (approved), its effect intent and confirm under the request's key, and
// the owner's consume row, with no row carrying the nonce.
func assertHeldGrantAudit(t *testing.T, d approvalDaemon, res policy.EnqueueResult) {
	t.Helper()
	page, err := d.Pol.Audit.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var queueRows []audit.Record
	for _, r := range page.Records {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), res.Token.Nonce) {
			t.Fatalf("audit row carries the nonce: %s", raw)
		}
		switch {
		case r.EffectKey == policy.GrantEffectKey(res.RequestID):
			got = append(got, "effect "+r.EffectPhase+" "+r.Outcome)
		case r.EffectKey == "" && (r.Kind == audit.KindApprovalGrant || r.Kind == audit.KindApprovalEnqueue):
			got = append(got, string(r.Kind)+" "+r.Outcome)
			queueRows = append(queueRows, r)
		}
	}
	// In order, with nothing else of these kinds between them: the enqueue,
	// the decision under its effect, then the owner's consume.
	want := []string{"approval.enqueue queued", "effect intent ", "approval.grant approved",
		"effect confirmed approved", "approval.grant consumed"}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, strings.Join(want[1:], "|")) || !strings.HasPrefix(joined, want[0]) ||
		strings.Count(joined, "approval.grant") != 2 {
		t.Fatalf("audit rows = %q, want %q in order", got, want)
	}
	assertQueueRowsLinked(t, queueRows)
}

// assertQueueRowsLinked: the queue's decision and consume rows carry no
// request id (P1-BF-R278), so they must carry the Actor, Action and
// ParamsHash of the granted request's enqueue row (the last enqueue before
// the decision), and a ParamsHash that differs from the fresh request's
// enqueue row (the first enqueue after the consume).
func assertQueueRowsLinked(t *testing.T, rows []audit.Record) {
	t.Helper()
	var granted, fresh *audit.Record
	byOutcome := map[string]audit.Record{}
	for i := range rows {
		r := rows[i]
		switch {
		case r.Kind == audit.KindApprovalEnqueue && len(byOutcome) == 0:
			granted = &rows[i]
		case r.Kind == audit.KindApprovalEnqueue && len(byOutcome) == 2 && fresh == nil:
			fresh = &rows[i]
		case r.Kind == audit.KindApprovalGrant:
			byOutcome[r.Outcome] = r
		}
	}
	if granted == nil || fresh == nil || len(byOutcome) != 2 {
		t.Fatalf("queue rows: granted %v, fresh %v, decision/consume %v", granted, fresh, byOutcome)
	}
	if granted.ParamsHash == "" || granted.ParamsHash == fresh.ParamsHash {
		t.Fatalf("enqueue params hashes %q and %q must be set and differ", granted.ParamsHash, fresh.ParamsHash)
	}
	for outcome, r := range byOutcome {
		if r.Actor != granted.Actor || r.Action != granted.Action || r.ParamsHash != granted.ParamsHash {
			t.Fatalf("%s row (%s %s %s) does not carry the granted request's enqueue row (%s %s %s)",
				outcome, r.Actor, r.Action, r.ParamsHash, granted.Actor, granted.Action, granted.ParamsHash)
		}
	}
}

func TestApprovalGrantCLIWithoutTokenApproves(t *testing.T) {
	d := newApprovalDaemon(t, nil)
	socket := serveRegistry(t, d.Registry)
	previous := approvalGrantCallDeps
	approvalGrantCallDeps = func(stderr io.Writer) elevatedCallDeps {
		return elevatedCallDeps{Custody: testCustody(t, d.KS, elevation.CustodyPlatform), Clock: runtime.NewSystemClock(),
			Getenv: func(string) string { return "" }, Stderr: stderr}
	}
	t.Cleanup(func() { approvalGrantCallDeps = previous })
	deps := hermeticApprovalDeps(t)
	deps.DialContext = redirectDial(client.UnixDialer, socket)
	a, b := d.enqueueL3(t, "fake merge a"), d.enqueueL3(t, "fake merge b")
	out, err := execLeaf(t, newApprovalGrantCmd(deps), []string{a.RequestID})
	want := "request " + a.RequestID + " approved; the requester that queued it redeems it"
	if err != nil || strings.TrimSpace(out) != want || strings.Contains(out, "redeemed") {
		t.Fatalf("approval grant <id> = %q, %v; want %q", out, err, want)
	}
	jsonOut, err := execLeaf(t, newApprovalGrantCmd(deps), []string{b.RequestID, "--json"})
	var env struct{ Data map[string]any }
	if err != nil || json.Unmarshal([]byte(jsonOut), &env) != nil {
		t.Fatalf("approval grant --json = %q, %v", jsonOut, err)
	}
	keys := make([]string, 0, len(env.Data))
	for k := range env.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "request_id,state" || env.Data["request_id"] != b.RequestID || env.Data["state"] != "approved" {
		t.Fatalf("approval grant --json data = %v, want exactly {request_id, state: approved}", env.Data)
	}
	assertShowCLI(t, deps, a.RequestID, "approved")
	if d.ownerRedeem(a, "fake merge a") != nil {
		t.Fatal("the CLI-approved request was not redeemable once by its owner")
	}
	assertShowCLI(t, deps, a.RequestID, "consumed")
	_, err = execLeaf(t, newApprovalGrantCmd(deps), []string{a.RequestID})
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || d.showState(t, a.RequestID) != "consumed" {
		t.Fatalf("second CLI grant = %v, want the permission-denied refusal and the entry still consumed", err)
	}
}

// assertShowCLI runs `approval show <id>` against the daemon and checks the
// table and the --json data both report state, with exactly the five keys.
func assertShowCLI(t *testing.T, deps approvalDeps, id, state string) {
	t.Helper()
	out, err := execLeaf(t, newApprovalShowCmd(deps), []string{id})
	if err != nil || !regexp.MustCompile(`(?m)^state\s+`+state+`$`).MatchString(out) || !strings.Contains(out, id) {
		t.Fatalf("approval show %s = %q, %v; want a state row %q", id, out, err, state)
	}
	jsonOut, err := execLeaf(t, newApprovalShowCmd(deps), []string{id, "--json"})
	var env struct{ Data map[string]any }
	if err != nil || json.Unmarshal([]byte(jsonOut), &env) != nil {
		t.Fatalf("approval show --json = %q, %v", jsonOut, err)
	}
	keys := make([]string, 0, len(env.Data))
	for k := range env.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "action_class,action_summary,exp,request_id,state" || env.Data["state"] != state {
		t.Fatalf("approval show --json data = %v, want the five keys and state %q", env.Data, state)
	}
}
