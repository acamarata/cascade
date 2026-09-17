package sync

// Purpose (this file): the resolve half of the sync.* RPC suite — the
//   elevation gate and every refusal around it — split from rpc_test.go
//   under the 300-line file cap. The cut is at the verb that changes
//   something versus the three that only answer questions.
// SPORT: internal/sync rpc resolve tests (ADD) — P1-E17-W4-S38-T3.

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSyncRpcElevationGate is the ticket's named check: discarding the
// server's copy is gated, keeping it is not.
func TestSyncRpcElevationGate(t *testing.T) {
	t.Run("keeping the server is not elevated", func(t *testing.T) {
		gate := &allowingGate{}
		res, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, gate)),
			MethodConflictsResolve, `{"record_id":"k","keep":"server"}`)
		if errObj != nil {
			t.Fatalf("accepting the merge's own decision was refused: %+v", errObj)
		}
		var got ResolveResult
		into(t, res, &got)
		if got.Elevated {
			t.Error("re-affirming what the merge already decided asked for authorization")
		}
		if len(gate.verbs) != 0 {
			t.Errorf("the gate was consulted for a no-op: %v", gate.verbs)
		}
	})

	t.Run("discarding the server is elevated", func(t *testing.T) {
		gate := &allowingGate{}
		res, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, gate)),
			MethodConflictsResolve, `{"record_id":"k","keep":"local"}`)
		if errObj != nil {
			t.Fatalf("an authorized resolve was refused: %+v", errObj)
		}
		var got ResolveResult
		into(t, res, &got)
		if !got.Elevated {
			t.Error("the result does not record that the gate ran")
		}
		if len(gate.verbs) != 1 || gate.verbs[0] != ElevatedVerbResolve {
			t.Errorf("the gate saw %v, want exactly %q", gate.verbs, ElevatedVerbResolve)
		}
	})

	assertElevationRefusals(t)
}

// assertElevationRefusals holds TestSyncRpcElevationGate's two refusal
// cases, split out under the 50-line function cap.
func assertElevationRefusals(t *testing.T) {
	t.Helper()
	t.Run("a denied elevation refuses", func(t *testing.T) {
		denied := cascade.New(cascade.KindPermissionDenied, "not authorized")
		_, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, refusingGate{denied})),
			MethodConflictsResolve, `{"record_id":"k","keep":"local"}`)
		if errObj == nil {
			t.Fatal("a denied elevation discarded the server's copy anyway")
		}
	})

	t.Run("no gate wired refuses", func(t *testing.T) {
		_, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, nil)),
			MethodConflictsResolve, `{"record_id":"k","keep":"local"}`)
		if errObj == nil {
			t.Fatal("a machine with no gate allowed the server's copy to be discarded")
		}
		if !strings.Contains(errObj.Message, "authorization") {
			t.Errorf("error %q does not say what is missing", errObj.Message)
		}
	})
}

// TestResolvingAnUnknownConflictIsRefused covers the boundary: a resolve
// for a conflict nobody journaled would otherwise report success for
// settling nothing.
func TestResolvingAnUnknownConflictIsRefused(t *testing.T) {
	_, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, &allowingGate{})),
		MethodConflictsResolve, `{"record_id":"never-journaled","keep":"server"}`)
	if errObj == nil {
		t.Fatal("a resolve for an unknown conflict succeeded")
	}
}

// TestAnUnrecognisedSideIsRefused proves the choice is closed, and says so
// in a way a non-interactive caller can act on.
func TestAnUnrecognisedSideIsRefused(t *testing.T) {
	_, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, &allowingGate{})),
		MethodConflictsResolve, `{"record_id":"k","keep":"whichever"}`)
	if errObj == nil {
		t.Fatal("an unrecognised side was accepted")
	}
	for _, want := range []string{KeepServer, KeepLocal} {
		if !strings.Contains(errObj.Message, want) {
			t.Errorf("error %q does not name the side %q", errObj.Message, want)
		}
	}
}

// TestAMisspeltParamIsRefusedNotIgnored is why unknown fields are an
// error: a caller that sent `domian` asked for one domain and would
// otherwise silently sync all of them.
func TestAMisspeltParamIsRefusedNotIgnored(t *testing.T) {
	if _, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, nil)),
		MethodRun, `{"domian":"config"}`); errObj == nil {
		t.Fatal("a misspelt parameter was ignored and every domain would have synced")
	}
}

// TestConflictsListReadsTheJournal joins the RPC to the merge's own
// record.
func TestConflictsListReadsTheJournal(t *testing.T) {
	res, errObj := syncCall(t, syncRegistry(rpcDeps(nodes.TierController, nil)), MethodConflictsList, `{}`)
	if errObj != nil {
		t.Fatalf("sync.conflicts_list: %+v", errObj)
	}
	var got ConflictsResult
	into(t, res, &got)
	if len(got.Conflicts) != 1 {
		t.Fatalf("%d conflict(s), want 1", len(got.Conflicts))
	}
	if got.Conflicts[0].Loser.Hash != "hl" {
		t.Errorf("the listed conflict does not name the discarded write: %+v", got.Conflicts[0])
	}
}
