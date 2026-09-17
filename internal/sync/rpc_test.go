package sync

// Purpose (this file): the four sync.* methods over a real rpc.Registry,
//   and the gate on the one verb that discards data.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T3.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
)

// refusingGate denies every elevated verb.
type refusingGate struct{ err error }

func (g refusingGate) Authorize(context.Context, string) error { return g.err }

// allowingGate records the verb it was asked about.
type allowingGate struct{ verbs []string }

func (g *allowingGate) Authorize(_ context.Context, verb string) error {
	g.verbs = append(g.verbs, verb)
	return nil
}

// syncRegistry mounts the four methods over deps.
func syncRegistry(deps Deps) *rpc.Registry {
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, deps)
	return reg
}

// syncCall dispatches one real JSON-RPC frame.
func syncCall(t *testing.T, reg *rpc.Registry, method, params string) (any, *rpc.ErrorObject) {
	t.Helper()
	raw := `{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `,"id":1}`
	req, errObj := rpc.Parse([]byte(raw))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	return reg.Dispatch(context.Background(), req)
}

// into round-trips a handler result into out, as the transport does.
func into(t *testing.T, from any, out any) {
	t.Helper()
	raw, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

// rpcDeps builds a surface with one journaled conflict.
func rpcDeps(tier nodes.Tier, gate ElevationGate) Deps {
	e := mergeEngine()
	e.Conflicts().Record(Conflict{
		Domain: "config", Subkind: "config", RecordID: "k",
		Strategy: StrategyServerPrimaryLWW, Resolution: ResolutionServerWon,
		Winner: Side{NodeID: "server", Revision: 9, Hash: "hs"},
		Loser:  Side{NodeID: "laptop", Revision: 8, Hash: "hl"},
	})
	return Deps{
		Engine: e, PeerTier: tier, Gate: gate,
		Run: func(context.Context, storage.DomainID, string) error { return nil },
	}
}

// TestSyncStatusListsEveryDomainIncludingTheIneligible is the rule that
// makes status useful: a report that omitted the domains this peer may not
// sync would answer "why is memory not syncing?" by omitting memory.
func TestSyncStatusListsEveryDomainIncludingTheIneligible(t *testing.T) {
	reg := syncRegistry(rpcDeps(nodes.TierWorkerTrusted, nil))
	res, errObj := syncCall(t, reg, MethodStatus, `{}`)
	if errObj != nil {
		t.Fatalf("sync.status: %+v", errObj)
	}
	var got StatusResult
	into(t, res, &got)

	if len(got.Domains) != len(AllCoreClasses()) {
		t.Fatalf("%d domains reported, want every registered one (%d)", len(got.Domains), len(AllCoreClasses()))
	}
	byKind := map[string]DomainStatus{}
	for _, d := range got.Domains {
		byKind[d.Subkind] = d
	}
	if !byKind["config"].Eligible {
		t.Error("config is not eligible for a worker-trusted peer")
	}
	mem := byKind["memory"]
	if mem.Eligible {
		t.Error("memory is eligible for a worker-trusted peer")
	}
	if mem.Reason == "" {
		t.Error("an ineligible domain reports no reason; that is the fact an operator needs")
	}
	if got.OpenConflicts != 1 {
		t.Errorf("open conflicts = %d, want 1", got.OpenConflicts)
	}
	if got.PeerTier != string(nodes.TierWorkerTrusted) {
		t.Errorf("peer tier = %q; a reader cannot tell which question was answered", got.PeerTier)
	}
}

// TestSyncStatusIsOrderedDeterministically keeps the surface diffable.
func TestSyncStatusIsOrderedDeterministically(t *testing.T) {
	reg := syncRegistry(rpcDeps(nodes.TierController, nil))
	var first []string
	for i := range 10 {
		res, errObj := syncCall(t, reg, MethodStatus, `{}`)
		if errObj != nil {
			t.Fatalf("sync.status: %+v", errObj)
		}
		var got StatusResult
		into(t, res, &got)
		var order []string
		for _, d := range got.Domains {
			order = append(order, d.Domain+"/"+d.Subkind)
		}
		if i == 0 {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d ordered domains differently:\n%v\nvs\n%v", i, order, first)
		}
	}
}

// TestSyncRunRefusesAnUnknownDomain is the rule that a caller who typed
// something asked for something. Reporting success for having done nothing
// is the shape of a bug they will not find for weeks.
func TestSyncRunRefusesAnUnknownDomain(t *testing.T) {
	reg := syncRegistry(rpcDeps(nodes.TierController, nil))
	_, errObj := syncCall(t, reg, MethodRun, `{"domain":"not-a-domain"}`)
	if errObj == nil {
		t.Fatal("an unknown domain reported a successful run")
	}
	if !strings.Contains(errObj.Message, "registered") {
		t.Errorf("error %q does not name what IS registered", errObj.Message)
	}
}

// TestSyncRunWithNoRunPathRefuses proves an unwired surface says so
// instead of reporting a sync that never happened.
func TestSyncRunWithNoRunPathRefuses(t *testing.T) {
	deps := rpcDeps(nodes.TierController, nil)
	deps.Run = nil
	if _, errObj := syncCall(t, syncRegistry(deps), MethodRun, `{}`); errObj == nil {
		t.Fatal("a run with no run path reported success")
	}
}

// TestSyncRunReportsPerDomainOutcomes is why one failure does not stop the
// others: a sync that abandoned four healthy domains because the fifth's
// remote was down would make a fleet wait on one machine.
func TestSyncRunReportsPerDomainOutcomes(t *testing.T) {
	deps := rpcDeps(nodes.TierController, nil)
	deps.Run = func(_ context.Context, _ storage.DomainID, subkind string) error {
		if subkind == "blobs" {
			return errors.New("the remote refused the connection")
		}
		return nil
	}
	res, errObj := syncCall(t, syncRegistry(deps), MethodRun, `{}`)
	if errObj != nil {
		t.Fatalf("one failing domain failed the whole run: %+v", errObj)
	}
	var got RunResult
	into(t, res, &got)

	var synced, failed int
	for _, d := range got.Domains {
		if d.Synced {
			synced++
			continue
		}
		failed++
		if d.Error == "" {
			t.Errorf("%s/%s did not sync and reports no reason", d.Domain, d.Subkind)
		}
	}
	if failed != 1 || synced == 0 {
		t.Errorf("%d synced, %d failed; want one failure and the rest through", synced, failed)
	}
}
