// Purpose: the real-counterpart proof CR item 5 demands (s48t4-cr-verdict.txt
//
//	S2): telegramApprovalService's exact wire shapes — Grant submits
//	{request_id} alone, Deny submits {request_id} alone — driven through the
//	REAL policy.MethodHandlers set, over a REAL approval queue (a real SQLite
//	store on a t.TempDir() file, Art.2, mirroring internal/policy's own
//	approval_queue_test.go fixture), with NO Verifier and NO Attestor wired —
//	the SAME composition cmd/cascade/daemon_unix_policy.go's wirePolicy
//	documents as today's honest, fail-closed production state.
//
// Inputs: none from outside; this file builds its own queue/registry/grants
//
//	per test.
//
// Outputs: two tests. The approve path proves telegram_approval_wiring.go's
//
//	own HONEST GAP header is true of the REAL handler, not just of the fake
//	approval_test.go drove before this ticket: the approve tap is refused
//	with KindElevationRequired, never a fake success. The deny path proves
//	the SAME real handler genuinely redeems: the entry actually leaves the
//	pending set.
//
// Constraints: Verifier and Attestor are left absent on purpose — attaching
//
//	either would make an unauthorized redemption look like it succeeded.
//
// SPORT: internal/plugins:cascadepa-approval-realcounterpart (ADD) —
//
//	P1-E23-W5-S48-T4 rework, T0 D1 item 5.
package plugins

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// realCounterpartCapability is the one capability this file's queue
// admits against, mirroring internal/policy's own approvalCap() fixture
// (a different package cannot reuse that unexported test helper).
const realCounterpartCapability = "workspace.write"

// newRealApprovalHandlers builds the REAL production handler set: a real
// SQLite-backed queue (never an in-memory double, Art.2), a real capability
// registry, real grant storage, and NO Verifier/Attestor — the exact gap
// telegram_approval_wiring.go's header names as today's honest state.
func newRealApprovalHandlers(t *testing.T) (map[string]policy.MethodFunc, *policy.StoreApprovals) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	reg := policy.NewMemoryRegistry()
	if err := reg.Add(context.Background(), policy.Capability{
		Name: realCounterpartCapability, Desc: "write files in the workspace",
		DefaultPolicy: policy.ClassWorkspaceMutation,
	}); err != nil {
		t.Fatalf("registering the capability: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	grants, err := policy.NewStoreGrants(db, reg, clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	queue, err := policy.NewApprovalQueue(policy.ApprovalQueueConfig{
		Store: db, Registry: reg, Grants: grants, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}
	handlers := policy.MethodHandlers(policy.RPCDeps{
		Queue: queue, Registry: reg, Grants: grants, Clock: clock,
		// Verifier and Attestor are deliberately absent — see this file's
		// header and cmd/cascade/daemon_unix_policy.go's identical choice.
	})
	return handlers, queue
}

// enqueueRealApproval admits one L2 action, the shape a real bridge
// approval would be queued against.
func enqueueRealApproval(t *testing.T, queue *policy.StoreApprovals) policy.EnqueueResult {
	t.Helper()
	res, err := queue.Enqueue(context.Background(), policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "s48t4-real-counterpart"},
		Capability: realCounterpartCapability,
		Level:      policy.L2,
		Action:     "edit workspace file",
		Params:     []byte(`{"path":"a.txt"}`),
		Summary:    "write a.txt",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return res
}

// TestApprovalGrant_RealHandlers_RefusesElevationRequired drives
// telegramApprovalService.Grant's EXACT wire shape — ApprovalGrantParams
// with RequestID set and SignedToken empty — through the real
// approval.grant handler. approval.grant is elevation-class
// (internal/rpc's elevationTable), so the guard middleware (RPCDeps.guard,
// internal/policy/rpc.go) refuses it with KindElevationRequired before the
// handler body ever runs — proving the approve path really is refused for
// the real reason, never a fake success, and that the refusal reaches the
// caller BEFORE anything about the request/token is even inspected.
func TestApprovalGrant_RealHandlers_RefusesElevationRequired(t *testing.T) {
	handlers, queue := newRealApprovalHandlers(t)
	res := enqueueRealApproval(t, queue)

	params, err := json.Marshal(policy.ApprovalGrantParams{RequestID: res.RequestID})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	handler, ok := handlers["approval.grant"]
	if !ok {
		t.Fatal(`handlers["approval.grant"] missing`)
	}
	_, err = handler(context.Background(), params)
	if err == nil {
		t.Fatal("approval.grant with no attestation source = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Errorf("approval.grant refusal kind = %v, want elevation-required", err)
	}
}

// TestApprovalDeny_RealHandlers_RedeemsForReal drives
// telegramApprovalService.Deny's EXACT wire shape — ApprovalDecisionParams
// with only RequestID set, no PresentedSummary/PresentedLevel, which is
// what Deny actually submits — through the real approval.deny handler.
// Deny is not elevation-class, so the guard passes it through, and the
// real queue.Decide records a genuine denial: the entry leaves the
// pending set, proving this is a real redemption, not a stand-in.
func TestApprovalDeny_RealHandlers_RedeemsForReal(t *testing.T) {
	handlers, queue := newRealApprovalHandlers(t)
	res := enqueueRealApproval(t, queue)

	params, err := json.Marshal(policy.ApprovalDecisionParams{RequestID: res.RequestID})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	handler, ok := handlers["approval.deny"]
	if !ok {
		t.Fatal(`handlers["approval.deny"] missing`)
	}
	out, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("approval.deny: %v", err)
	}
	result, ok := out.(policy.DecisionResult)
	if !ok {
		t.Fatalf("approval.deny result = %#v (%T), want policy.DecisionResult", out, out)
	}
	if result.RequestID != res.RequestID || result.State != "denied" {
		t.Errorf("approval.deny result = %+v, want RequestID=%s State=denied", result, res.RequestID)
	}

	pending, err := queue.GetPending(context.Background())
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	for _, p := range pending {
		if p.RequestID == res.RequestID {
			t.Errorf("request %s is still pending after a real deny redemption", res.RequestID)
		}
	}
}
