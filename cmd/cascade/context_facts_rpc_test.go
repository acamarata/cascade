//go:build !windows

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// factsRecorder is a recording provider.ModelExecutor.
type factsRecorder struct {
	reqs   []provider.ModelRequest
	output string
}

func (r *factsRecorder) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	r.reqs = append(r.reqs, req)
	return provider.ModelResponse{Output: r.output}, nil
}

// factsTestDeps builds handler deps over paths with exec and a recording
// admission hook that lists every path handed to the guard (and so opened).
func factsTestDeps(paths runtime.PathProvider, exec provider.ModelExecutor, opened *[]string) contextFactsDeps {
	return contextFactsDeps{
		Paths: paths, Clock: runtime.NewSystemClock(), Exec: exec, Tracked: factsTrackedFiles,
		Admit: func(ctx context.Context, root string, rels []string) ([]repo.Admission, error) {
			*opened = append(*opened, rels...)
			if !gitRunnable() {
				return factsFixtureAdmission(root, rels)
			}
			return repo.AdmitBatch(ctx, root, rels)
		},
	}
}

// seedProposedFact writes repo-1 and one proposed layout fact under paths.
func seedProposedFact(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	id := repo.FactID("repo-1", repo.SubjectLayout)
	err := withRepoDB(context.Background(), paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		ctx := context.Background()
		if err := scope.NewGraphStore(db).PutRepository(ctx, scope.RepositoryRecord{ID: "repo-1", PathHash: "abc"}); err != nil {
			return err
		}
		return repo.NewLedgerStore(db).Append(ctx, repo.InferredFact{
			ID: id, RepositoryID: "repo-1", Subject: repo.SubjectLayout, Fact: "flat",
			Source: string(provider.ProvenanceUntrustedSource), Confidence: 0.5, Version: 1, State: repo.FactProposed,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ledgerRows lists repo-1's rows under paths.
func ledgerRows(t *testing.T, paths runtime.PathProvider) []repo.InferredFact {
	t.Helper()
	var rows []repo.InferredFact
	err := withRepoDB(context.Background(), paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		var err error
		rows, err = repo.NewLedgerStore(db).List(context.Background(), repo.LedgerFilter{RepositoryID: "repo-1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func wantFactsKind(t *testing.T, err error, want cascade.Kind, msg string) {
	t.Helper()
	k, ok := cascade.KindOf(err)
	if !ok || k != want || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v, want kind %v containing %q", err, want, msg)
	}
}

func TestBuildRPCServerRegistersContextFacts(t *testing.T) {
	deps := policyDeps(t, "")
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil || reg == nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	served := map[string]bool{}
	for _, m := range reg.Methods() {
		served[m] = true
	}
	for _, m := range []string{contextFactsListMethod, contextFactsProposeMethod, contextFactsAcceptMethod} {
		if !served[m] {
			t.Errorf("the composed daemon does not serve %s", m)
		}
	}
}

func TestFactsAcceptWithoutWitnessRefused(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	reg, call := newElevatedTestRegistry(t, dataDir)
	paths := elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: filepath.Dir(dataDir)}, data: dataDir}
	id := seedProposedFact(t, paths)
	params, _ := json.Marshal(map[string]string{"id": id})
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, eo := reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: contextFactsAcceptMethod, Params: params}); eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unsigned accept = %+v, want an elevation challenge", eo)
	}
	d := factsTestDeps(paths, &factsRecorder{}, new([]string))
	for _, raw := range []string{string(params), `{"id":"` + id + `","_verified":true}`} {
		_, err := d.accept(owner, json.RawMessage(raw))
		wantFactsKind(t, err, cascade.KindElevationRequired, "needs a verified elevation")
	}
	if rows := ledgerRows(t, paths); len(rows) != 1 || rows[0].State != repo.FactProposed {
		t.Fatalf("an unwitnessed accept changed the ledger: %+v", rows)
	}
	var out struct{ Fact repo.InferredFact }
	if err := call(context.Background(), contextFactsAcceptMethod, map[string]string{"id": id}, &out); err != nil {
		t.Fatalf("signed accept: %v", err)
	}
	if out.Fact.State != repo.FactAccepted || out.Fact.AcceptedBy != repo.AcceptedByOperator || out.Fact.Version != 2 {
		t.Fatalf("signed accept recorded %+v", out.Fact)
	}
}

func TestFactsAcceptedByNeverFromParams(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	_, call := newElevatedTestRegistry(t, dataDir)
	paths := elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: filepath.Dir(dataDir)}, data: dataDir}
	id := seedProposedFact(t, paths)
	err := call(context.Background(), contextFactsAcceptMethod, map[string]string{"id": id, "accepted_by": "detector:languages"}, nil)
	if err == nil || !strings.Contains(err.Error(), "accepted_by") {
		t.Fatalf("accept with accepted_by in params = %v, want an invalid-input refusal naming it", err)
	}
	if eo, ok := err.(*rpc.ErrorObject); !ok || eo.Code != cascade.NewRPCError(cascade.New(cascade.KindInvalidInput, "x")).Code {
		t.Fatalf("refusal = %#v, want the KindInvalidInput code", err)
	}
	if rows := ledgerRows(t, paths); len(rows) != 1 || rows[0].State != repo.FactProposed {
		t.Fatalf("a refused accept changed the ledger: %+v", rows)
	}
}

func TestFactsProposeRunsExtractIntake(t *testing.T) {
	paths := factsTestEnv(t)
	dir := factsGitRepo(t, map[string]string{"README.md": "# widget\nA tool.\n"})
	exec := &factsRecorder{output: `{"proposals":[{"subject":"harness_files","fact":"none present","confidence":0.6}]}`}
	var opened []string
	d := factsTestDeps(paths, exec, &opened)
	raw, _ := json.Marshal(map[string]string{"repo": dir})
	got, err := d.propose(context.Background(), raw)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	res := got.(factsProposeResult)
	if len(exec.reqs) != 1 || len(res.Proposed) != 1 || res.Proposed[0].Subject != repo.SubjectHarnessFiles || res.Refused != 0 {
		t.Fatalf("propose = %+v after %d requests, want one proposed harness_files row", res, len(exec.reqs))
	}
	inv, ok := storedInventory(t, paths, res.Proposed[0].RepositoryID)
	if !ok || inv.Repository.ID != res.Proposed[0].RepositoryID {
		t.Fatal("propose did not register the repository's inventory first")
	}
	listed, err := d.list(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if facts := listed.(map[string]any)["facts"].([]repo.InferredFact); len(facts) != 1 || facts[0].State != repo.FactProposed {
		t.Fatalf("list = %+v, want the proposed row", facts)
	}
	for _, bad := range []string{`{"repo":"relative"}`, `{"repo":""}`, `{}x`} {
		if _, err := d.propose(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("propose(%s) succeeded", bad)
		}
	}
}
