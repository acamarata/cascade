package daemon

// Purpose (this file): the production recovery deps, driven through the
//   REAL nodes.PlanRequeue — the decision the daemon's dispatch verb makes
//   when a node goes away, over the real record store, the real journal
//   and the real attention queue.
// WHY NOT THROUGH THE RPC FRAME: reaching PlanRequeue through node.dispatch
//   needs the ship leg to fail loss-shaped first, which needs a real git
//   remote and a real node that stops answering — that lane exists and is
//   tagged (internal/nodes TestKillNodeMidRun). What is NOT covered there,
//   and is covered here, is the daemon's own composition: every
//   collaborator below is the one a shipping binary builds, and before
//   P1-E17-W4-S37-T6 two of them were nil and this decision refused.
// SPORT: internal/daemon status:requeue-collaborators (ADD tests) —
//   P1-E17-W4-S37-T6.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// requeueFixture is one daemon's worth of real recovery collaborators.
type requeueFixture struct {
	records    *nodes.RecordStore
	dispatcher *nodes.Dispatcher
	recovery   RecoveryStores
	clock      runtime.Clock
}

// newRequeueFixture enrolls a lost node and a healthy spare, streams one
// record into the real journal, and returns the composition.
//
// The records are written straight to the file backend because LastSeen is
// a HEARTBEAT's output and this test is not about heartbeat verification:
// what it needs is a fleet where one node was heard from recently and one
// was not, which is exactly what those two timestamps mean.
func newRequeueFixture(t *testing.T) requeueFixture {
	t.Helper()
	clock := runtime.NewSystemClock()
	dataDir := t.TempDir()
	now := clock.Now()
	writeDeviceRecords(t, dataDir, map[string]nodes.DeviceRecord{
		// The prober's last verdict on each. Unknown is the fail-closed
		// value a heartbeat timeout leaves behind, which is exactly what
		// the lost node has.
		"lost": {
			NodeID: "lost", Tier: nodes.TierWorkerTrusted, EnrolledAt: now,
			LastSeen: now.Add(-nodes.DefaultHeartbeatTimeout * 4),
			Presence: nodes.PresenceUnknown,
		},
		"spare": {
			NodeID: "spare", Tier: nodes.TierWorkerTrusted, EnrolledAt: now,
			LastSeen: now, Presence: nodes.PresenceReachable,
		},
	})

	jstore := journal.New(storetest.NewMemStore(), clock, "nodes.dispatch")
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), clock)
	registry := rpc.NewRegistry()
	recovery := RecoveryStores{
		Journal:   jstore,
		Attention: NewAttentionStore(storetest.NewMemStore(), clock, nil),
	}
	dispatcher, _ := RegisterNodeDispatchHandlers(registry, records, clock,
		func() (nodes.Section, error) { return configuredSection(), nil }, recovery)
	RegisterNodeDispatchJournal(registry, dispatcher, jstore)

	attempt := dispatcher.Attempts().Next("d-e2e")
	streamRecordFor(t, registry, "d-e2e", "job-e2e", attempt)
	return requeueFixture{records: records, dispatcher: dispatcher, recovery: recovery, clock: clock}
}

// writeDeviceRecords seeds the file backend the production store reads.
func writeDeviceRecords(t *testing.T, dataDir string, recs map[string]nodes.DeviceRecord) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nodes", "devices.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// streamRecordFor sends one record through the real journal verb.
func streamRecordFor(t *testing.T, registry *rpc.Registry, dispatchID, entityID string, attempt uint64) {
	t.Helper()
	params, err := json.Marshal(nodes.JournalRecord{
		DispatchID: dispatchID, Attempt: attempt, EntityID: entityID,
		OperationID: "op-before-the-loss", Payload: json.RawMessage(`{"note":"work"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, errObj := registry.Dispatch(context.Background(),
		&rpc.Request{Method: nodes.DispatchJournalMethod, Params: params}); errObj != nil {
		t.Fatalf("streaming a journal record: %+v", errObj)
	}
}

// requeueRequest is the loss the fixture is about.
func requeueRequest() nodes.RequeueRequest {
	return nodes.RequeueRequest{
		DispatchID: "d-e2e", LostNodeID: "lost",
		Action:      nodes.Action{ID: "a-1", Idempotent: true},
		Requirement: nodes.Requirement{Sensitivity: nodes.SensitivityNormal},
		EntityID:    "job-e2e",
	}
}

// TestTheDaemonsOwnDepsRequeueALostNode is the acceptance: the work moves
// to the node that did not go away, at a FENCED attempt, resuming from
// what the lost attempt actually recorded.
func TestTheDaemonsOwnDepsRequeueALostNode(t *testing.T) {
	f := newRequeueFixture(t)
	deps := resolveRequeueDeps(f.dispatcher, f.records, f.clock, f.recovery)

	plan, err := nodes.PlanRequeue(context.Background(), deps, requeueRequest(), nodes.LossHeartbeat)
	if err != nil {
		t.Fatalf("the daemon's own deps could not re-queue a lost node: %v", err)
	}
	if plan.Disposition != nodes.DispositionRequeue {
		t.Fatalf("disposition = %q, want a re-queue", plan.Disposition)
	}
	if plan.Node.NodeID != "spare" {
		t.Errorf("replacement = %q, want the node that was still heartbeating", plan.Node.NodeID)
	}
	if plan.Attempt != 2 {
		t.Errorf("replacement attempt = %d, want 2 — an unfenced replacement races the attempt it replaces",
			plan.Attempt)
	}
	if plan.Resume.FromScratch {
		t.Error("the replacement resumes from scratch; the journal had a record and it was not read")
	}
	if plan.Resume.Seq == 0 {
		t.Error("resume sequence = 0; the replacement would re-run the work the lost attempt recorded")
	}
	if len(plan.Resume.CompletedOperations) == 0 {
		t.Error("no completed operations carried; a re-delivered operation would be re-run")
	}
}

// TestWithoutTheStoresTheSameLossIsOnlyReported is the mutation proof for
// the wiring this ticket added: the identical fixture, with the two stores
// absent, REFUSES — which is what a daemon did before S-37.T6, and which
// is why that state was a recorded gap rather than a silent one.
func TestWithoutTheStoresTheSameLossIsOnlyReported(t *testing.T) {
	f := newRequeueFixture(t)
	deps := resolveRequeueDeps(f.dispatcher, f.records, f.clock, RecoveryStores{})

	_, err := nodes.PlanRequeue(context.Background(), deps, requeueRequest(), nodes.LossHeartbeat)
	if err == nil {
		t.Fatal("a daemon with no journal and no attention queue re-queued anyway; " +
			"the replacement would have restarted work that was already done")
	}
}

// TestALostNodeIsNotItsOwnReplacement pins the rule that placement, not
// the candidate set, is what keeps a lost node from being handed its own
// work back: it is still enrolled and still offered, and the fleet-state
// filters are what exclude it.
func TestALostNodeIsNotItsOwnReplacement(t *testing.T) {
	f := newRequeueFixture(t)
	deps := resolveRequeueDeps(f.dispatcher, f.records, f.clock, f.recovery)
	if len(deps.Candidates) != 2 {
		t.Fatalf("%d candidates, want both enrolled nodes offered to placement", len(deps.Candidates))
	}
	plan, err := nodes.PlanRequeue(context.Background(), deps, requeueRequest(), nodes.LossHeartbeat)
	if err != nil {
		t.Fatalf("re-queue: %v", err)
	}
	if plan.Node.NodeID == "lost" {
		t.Error("the work went back to the node that was lost")
	}
}

// TestAHeldActionReachesTheQueueThroughTheDaemonsDeps covers the other
// disposition: work that must not be re-run is HELD, and the hold lands in
// the queue the operator's own verb reads.
func TestAHeldActionReachesTheQueueThroughTheDaemonsDeps(t *testing.T) {
	f := newRequeueFixture(t)
	deps := resolveRequeueDeps(f.dispatcher, f.records, f.clock, f.recovery)

	req := requeueRequest()
	req.Action = nodes.Action{ID: "a-1", Idempotent: false}
	plan, err := nodes.PlanRequeue(context.Background(), deps, req, nodes.LossTunnel)
	if err == nil {
		t.Fatalf("a non-idempotent action after a dropped tunnel was re-queued: %+v", plan)
	}
	// The refusal is the contract: HoldUnknownOutcome always returns one so
	// a caller cannot treat a held dispatch as a finished one.
	if !strings.Contains(err.Error(), "d-e2e") {
		t.Errorf("the hold does not name the dispatch: %v", err)
	}
	// And the hold is not only an error: it is in the queue.
	items, listErr := f.recovery.Attention.ListInScopes(context.Background(),
		[]supervision.ScopeRef{{Kind: scope.ScopeKindGlobal, ID: "fleet"}}, supervision.Filter{})
	if listErr != nil {
		t.Fatalf("listing the queue: %v", listErr)
	}
	if len(items) != 1 || items[0].SourceRef != "d-e2e" {
		t.Fatalf("the queue holds %+v; a held dispatch nobody was told about is the worse failure", items)
	}
}
