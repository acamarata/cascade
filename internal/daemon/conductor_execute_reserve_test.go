package daemon

// Purpose: conductor.execute meters every dispatch through the Executor's
//   own CallReserver: a client reservation_id never reaches the Executor,
//   and fresh and resumed fan-out legs reserve one row each with no parent
//   reservation id. No production code calls NewLegBudget.

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// clientReservation is the reservation id a client tries to smuggle in.
const clientReservation = "client-res"

// recordingReserver records the ReservationID each Reserve sees and every
// id Check is asked to resolve.
type recordingReserver struct {
	mu       sync.Mutex
	reserved []string
	checked  []string
}

func (r *recordingReserver) Reserve(_ context.Context, _ provider.Selection, req provider.ModelRequest) (conductor.HeldCall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved = append(r.reserved, req.ReservationID)
	return passHold{id: "row"}, nil
}

func (r *recordingReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (conductor.HeldCall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked = append(r.checked, id)
	return passHold{id: id}, nil
}

func (r *recordingReserver) snapshot() (reserved, checked []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reserved...), append([]string(nil), r.checked...)
}

// newReserveEnv is newFuEnv with a recording reserver in place of passReserver.
func newReserveEnv(t *testing.T) (*fuEnv, *recordingReserver) {
	t.Helper()
	st := &fuStore{MemStore: storetest.NewMemStore()}
	p, clock, reg := &fuProvider{st: st}, runtime.NewSystemClock(), rpc.NewRegistry()
	fan, rec := NewConductorFanOut(st, clock), &recordingReserver{}
	if err := RegisterConductorExecuteHandler(reg, NewManifest(nil, clock), p, p, p, newTestAuditWriter(t), clock, fullSecurity(t), ConductorAccounting{Reserver: rec}, fan, conductor.WithLaneTierCap(daemonTestCap)); err != nil {
		t.Fatal(err)
	}
	return &fuEnv{t: t, ctx: context.Background(), reg: reg, fan: fan, st: st, p: p}, rec
}

func TestConductorExecuteRPCClearsClientReservationID(t *testing.T) {
	e, rec := newReserveEnv(t)
	params := map[string]any{"task_id": "t", "task_class": "chat", "sensitivity": "public", "reservation_id": clientReservation,
		"inputs": []map[string]string{{"role": "user", "content": "prompt"}}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, eo := e.reg.Dispatch(e.ctx, &rpc.Request{Method: ConductorExecuteMethod, Params: raw}); eo != nil {
		t.Fatalf("conductor.execute: %+v, want a served dispatch", eo)
	}
	reserved, checked := rec.snapshot()
	if len(reserved) != 1 || reserved[0] != "" || len(checked) != 0 || e.p.calls.Load() != 1 {
		t.Fatalf("Reserve saw %q, Check saw %q, calls %d; want one self-reserved row with an empty id, no Check, one call",
			reserved, checked, e.p.calls.Load())
	}
	var wire conductorExecuteParams
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	req, err := wire.toModelRequest()
	if err != nil || req.ReservationID != "" || req.TaskID != "t" {
		t.Fatalf("toModelRequest = (%+v, %v); want the request with ReservationID cleared", req, err)
	}
}

func TestResumedLegsReserve(t *testing.T) {
	e, rec := newReserveEnv(t)
	e.p.err = errors.New("provider down")
	e.strand("final", "delete") // both legs fail and the fan-out stays open
	fresh, _ := rec.snapshot()
	e.check(len(fresh) == 2 && e.p.calls.Load() == 2, "fresh legs reserved %q over %d calls; want two rows", fresh, e.p.calls.Load())
	stored, found, err := e.fan.Results.GetRequest(e.ctx, fuID)
	e.check(err == nil && found, "stored request: found %v, %v", found, err)
	e.check(e.st.MemStore.Delete(e.ctx, "conductor.fanout.requests", fuID) == nil, "clearing the stored request")
	stored.ReservationID = clientReservation // a parent id must never reach a resumed leg
	e.check(e.fan.Results.PutRequest(e.ctx, fuID, stored) == nil, "re-storing the request with a parent id")
	e.p.err = nil
	got, eo := e.call(map[string]any{"reservation_id": clientReservation})
	reserved, checked := rec.snapshot()
	e.check(eo == nil && len(got.Legs) == 2 && e.p.calls.Load() == 4, "re-attach = (%+v, %+v), calls %d; want two resumed legs", got, eo, e.p.calls.Load())
	e.check(len(reserved) == 4 && len(checked) == 0, "rows %q, checks %q; want one row per fresh and resumed leg, no Check", reserved, checked)
	for i, id := range reserved {
		e.check(id == "", "row %d carried reservation id %q; want none", i, id)
	}
	assertNoProductionLegBudget(t)
}

// assertNoProductionLegBudget parses the module's non-test Go files and
// fails on any call of NewLegBudget, qualified or not.
func assertNoProductionLegBudget(t *testing.T) {
	t.Helper()
	root, seen := filepath.Join("..", ".."), 0
	for _, dir := range []string{"internal", "cmd", "pkg", "plugins", "providers"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			seen++
			if callsLegBudget(f) {
				t.Errorf("%s calls NewLegBudget; production fan-out must meter legs through the CallReserver", path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("no production Go file was scanned")
	}
}

// callsLegBudget reports whether f calls NewLegBudget or x.NewLegBudget.
func callsLegBudget(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			found = found || fn.Name == "NewLegBudget"
		case *ast.SelectorExpr:
			found = found || fn.Sel.Name == "NewLegBudget"
		}
		return !found
	})
	return found
}
