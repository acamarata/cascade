// Package pbd (lifecycle_test.go): lifecycle.go's tests — the real
// production entry point (handlers{}.RunCommand), the manifest mirror,
// and Art.2's real-client proof (internal/rpc.Registry/rpc.Parse, never
// self-authored). This ticket's journal records the manual proof that
// TestTicketLifecycleCommands can fail: removing RunCommand's
// claim/step/done case makes it fail with KindUnsupported.
// SPORT: plugins/pbd lifecycle (ADD) — P1-E14-W3-S30-T1.
package pbd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// mkLifecycleFixture writes a single valid ticket tree at the canonical
// N/W3/S30/T9 position and returns its root.
func mkLifecycleFixture(t *testing.T) (root, id string) {
	t.Helper()
	root = t.TempDir()
	id = "P1-E14-W3-S30-T9"
	dir := filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-30", "tickets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "T-9.yaml"), []byte(cleanLintTicketYAML(id)), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, id
}

func TestTicketLifecycleCommands(t *testing.T) {
	root, id := mkLifecycleFixture(t)
	h := handlers{}
	ctx := context.Background()

	if err := h.RunCommand(ctx, claimCommandName, []string{root, id, "op-claim"}); err != nil {
		t.Fatalf("RunCommand(claim): %v", err)
	}
	if err := h.RunCommand(ctx, stepCommandName, []string{root, id, "op-step", "", "", "", "", "wrote it"}); err != nil {
		t.Fatalf("RunCommand(step): %v", err)
	}
	// cleanLintTicketYAML declares cr_level CR-A+CR-B, qa_level QA-A.
	if err := h.RunCommand(ctx, stepCommandName, []string{root, id, "op-cr-a", "", "cr", "CR-A"}); err != nil {
		t.Fatalf("RunCommand(step, cr A): %v", err)
	}
	if err := h.RunCommand(ctx, stepCommandName, []string{root, id, "op-cr-b", "", "cr", "CR-B"}); err != nil {
		t.Fatalf("RunCommand(step, cr B): %v", err)
	}
	if err := h.RunCommand(ctx, stepCommandName, []string{root, id, "op-qa", "", "qa", "", "QA-A"}); err != nil {
		t.Fatalf("RunCommand(step, qa): %v", err)
	}
	if err := h.RunCommand(ctx, doneCommandName, []string{root, id, "op-done"}); err != nil {
		t.Fatalf("RunCommand(done): %v", err)
	}

	js := NewFileJournalStore(root, nil)
	state, serr := pews.CurrentState(ctx, js, id)
	if serr != nil || state != pews.StateDone {
		t.Fatalf("CurrentState = %v, %v, want done, nil", state, serr)
	}

	// Missing required args refuses without touching the journal.
	if err := h.RunCommand(ctx, claimCommandName, []string{root}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RunCommand(claim, no ticket): err = %v, want KindInvalidInput", err)
	}
	// An illegal transition (already done) still refuses through the CLI path.
	if err := h.RunCommand(ctx, claimCommandName, []string{root, id, "op-reclaim"}); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("RunCommand(reclaim after done): err = %v, want KindConflict", err)
	}
	if err := h.RunCommand(ctx, "bogus", []string{root, id, "op"}); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("RunCommand(bogus): err = %v, want KindUnsupported", err)
	}
}

// TestTicketLifecycleRealJSONRPCCounterpart is Art.2's requirement: a
// real rpc.Registry/rpc.Parse client, never a self-authored dialect.
func TestTicketLifecycleRealJSONRPCCounterpart(t *testing.T) {
	root, id := mkLifecycleFixture(t)
	reg := rpc.NewRegistry()
	reg.Register(claimRPCMethod, ClaimRPC)
	reg.Register(stepRPCMethod, StepRPC)
	reg.Register(doneRPCMethod, DoneRPC)

	dispatch := func(method string, req lifecycleRequest) any {
		t.Helper()
		body, merr := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": req, "id": 1})
		if merr != nil {
			t.Fatalf("marshal request: %v", merr)
		}
		parsed, perr := rpc.Parse(body)
		if perr != nil {
			t.Fatalf("rpc.Parse: %+v", perr)
		}
		result, derr := reg.Dispatch(context.Background(), parsed)
		if derr != nil {
			t.Fatalf("Dispatch(%s): %+v", method, derr)
		}
		return result
	}

	claimed := dispatch(claimRPCMethod, lifecycleRequest{Root: root, Ticket: id, OperationID: "op-1"})
	entry, ok := claimed.(pews.JournalEntry)
	if !ok || entry.Event != pews.EventClaim {
		t.Fatalf("claim result = %+v, ok=%v, want a claim JournalEntry", claimed, ok)
	}

	stepped := dispatch(stepRPCMethod, lifecycleRequest{Root: root, Ticket: id, OperationID: "op-2"})
	if e, ok := stepped.(pews.JournalEntry); !ok || e.Event != pews.EventStep {
		t.Fatalf("step result = %+v, ok=%v, want a step JournalEntry", stepped, ok)
	}

	// Skipping cr/qa: Done from StateStep must refuse, real client, real dispatch.
	body, merr := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": doneRPCMethod,
		"params": lifecycleRequest{Root: root, Ticket: id, OperationID: "op-3"}, "id": 1})
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	parsed, perr := rpc.Parse(body)
	if perr != nil {
		t.Fatalf("rpc.Parse: %+v", perr)
	}
	if _, derr := reg.Dispatch(context.Background(), parsed); derr == nil || derr.Code != cascade.RPCCodeConflict {
		t.Fatalf("Dispatch(done from step): err = %+v, want RPCCodeConflict", derr)
	}
}

func TestTicketLifecycleMirroredMethods(t *testing.T) {
	m := manifest()
	rpcByName := map[string]string{}
	for _, c := range m.Provides.Commands {
		rpcByName[c.Name] = c.RPCMethod
	}
	want := map[string]string{claimCommandName: claimRPCMethod, stepCommandName: stepRPCMethod, doneCommandName: doneRPCMethod}
	for name, method := range want {
		if rpcByName[name] != method {
			t.Errorf("RPCMethod[%q] = %q, want %q", name, rpcByName[name], method)
		}
	}
	for _, tool := range m.Provides.Tools {
		if tool.Name == claimCommandName || tool.Name == stepCommandName || tool.Name == doneCommandName {
			t.Errorf("lifecycle verb %q mounted as an MCP tool, want none (lifecycle writes are not MCP-safe)", tool.Name)
		}
	}
}

func TestTicketLifecycleErrorPaths(t *testing.T) {
	root, id := mkLifecycleFixture(t)
	ctx := context.Background()

	if _, err := decodeLifecycleRequest(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("decodeLifecycleRequest(nil): err = %v, want KindInvalidInput", err)
	}
	if _, err := decodeLifecycleRequest([]byte("{not json")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("decodeLifecycleRequest(malformed): err = %v, want KindInvalidInput", err)
	}
	if _, err := runLifecycle(ctx, stepCommandName, lifecycleRequest{Root: root, Ticket: id, OperationID: "op", Event: "bogus"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("runLifecycle(step, bogus event): err = %v, want KindInvalidInput", err)
	}
	if _, err := runLifecycle(ctx, "sideways", lifecycleRequest{Root: root, Ticket: id, OperationID: "op"}); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("runLifecycle(unknown verb): err = %v, want KindUnsupported", err)
	}
	// A root with no epics/ dir at all loads as a valid, empty tree
	// (store.go's readDirIfExists convention), so the ticket lookup itself
	// is what refuses.
	if _, err := runLifecycle(ctx, claimCommandName, lifecycleRequest{Root: filepath.Join(root, "nope"), Ticket: id, OperationID: "op"}); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("runLifecycle(empty tree, unknown ticket): err = %v, want KindNotFound", err)
	}
}
