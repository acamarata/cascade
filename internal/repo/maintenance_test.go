package repo

// Purpose: pipeline behavior tests driven through Apply: scoping, stale-fact
//   rejection, cursor ordering, vector_pending, idempotence, path isolation.
// SPORT: repo/maintenance-pipeline/ADD test coverage (P1-GEN-04).

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// mustIntents, mustPaths and mustCursor read the journal and fail the test on
// a read error, so a broken journal never passes an "is empty" assertion.
func mustIntents(t *testing.T, j *WatchJournal) []intent {
	t.Helper()
	in, err := j.intents(context.Background(), testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func mustPaths(t *testing.T, j *WatchJournal) map[string]PathState {
	t.Helper()
	p, err := j.Paths(context.Background(), testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustCursor(t *testing.T, j *WatchJournal) (Cursor, bool) {
	t.Helper()
	c, ok, err := j.Cursor(context.Background(), testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	return c, ok
}

const betaChanged = "package b\n\n// Beta is package b's exported func.\nfunc Beta() string { return \"beta2\" }\n\n// Extra is new.\nfunc Extra() int { return 1 }\n"

// packageNodesJSON is the stored graph's nodes and edges of one package.
func packageNodesJSON(t *testing.T, g SymbolGraph, pkg string) string {
	t.Helper()
	var sub SymbolGraph
	for _, n := range g.Nodes {
		if n.Package == pkg {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if packageOf(e.From) == pkg {
			sub.Edges = append(sub.Edges, e)
		}
	}
	data, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMaintenanceScopedToChangedPath(t *testing.T) {
	e := newMaintEnv(t)
	before, _, _, err := e.graph.Get(context.Background(), testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	var loads [][]string
	var fams []Language
	onGraphLoad = func(p []string) { loads = append(loads, p) }
	onDetect = func(l Language) { fams = append(fams, l) }
	t.Cleanup(func() { onGraphLoad, onDetect = func([]string) {}, func(Language) {} })

	e.write(t, "b/b.go", betaChanged)
	rep := e.apply(t, 1, "b/b.go")
	if !reflect.DeepEqual(rep.Applied, []string{"b/b.go"}) || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want only b/b.go applied", rep)
	}
	if !reflect.DeepEqual(loads, [][]string{{"./b"}}) {
		t.Fatalf("graph loads = %v, want exactly [./b] (never ./...)", loads)
	}
	if !reflect.DeepEqual(fams, []Language{LanguageGo}) {
		t.Fatalf("detector families rerun = %v, want only go", fams)
	}
	after, gen, _, err := e.graph.Get(context.Background(), testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"fixturemaint/a", "fixturemaint/c"} {
		if packageNodesJSON(t, before, pkg) != packageNodesJSON(t, after, pkg) {
			t.Errorf("package %s changed in the stored row", pkg)
		}
	}
	if !strings.Contains(packageNodesJSON(t, after, "fixturemaint/b"), "Extra") || gen != 2 {
		t.Errorf("package b not re-extracted (gen %d): %s", gen, packageNodesJSON(t, after, "fixturemaint/b"))
	}
	for _, c := range e.idx.Calls {
		if strings.HasPrefix(c, "fts:") && c != "fts:b/b.go" || strings.HasPrefix(c, "vec:") && c != "vec:b/b.go" {
			t.Errorf("index call %q outside the changed path", c)
		}
	}
	if !reflect.DeepEqual(e.idx.Graph, after) {
		t.Error("IndexLeg.WriteGraph did not receive the merged stored graph")
	}
}

func TestStaleFactInvalidationRejectsCitingFacts(t *testing.T) {
	e := newMaintEnv(t)
	ctx := context.Background()
	add := func(id, fact, source string, st FactState) {
		f := InferredFact{ID: id, RepositoryID: testRepoID, Subject: SubjectLanguages, Fact: fact, Source: source,
			Confidence: 0.5, Version: 1, State: st}
		if st == FactAccepted {
			f.AcceptedBy = "go-detector"
		}
		if err := e.ledger.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	add("f1", "see b/b.go for it", "scan", FactProposed)
	add("f2", "go", "scan of b/b.go:12", FactAccepted)
	add("f3", "uses a/a.go", "scan", FactProposed)
	add("f4", "xb/b.go is odd", "scan", FactProposed)
	add("f5", "b/b.go.bak", "src/b/b.go", FactProposed)
	e.write(t, "b/b.go", betaChanged)
	rep := e.apply(t, 1, "b/b.go")
	if !reflect.DeepEqual(rep.Invalidated, []string{"f1", "f2"}) {
		t.Fatalf("Invalidated = %v, want [f1 f2]", rep.Invalidated)
	}
	want := map[string]FactState{"f1": FactRejected, "f2": FactRejected, "f3": FactProposed, "f4": FactProposed, "f5": FactProposed}
	all, err := e.ledger.List(ctx, LedgerFilter{RepositoryID: testRepoID})
	if err != nil || len(all) != 5 {
		t.Fatalf("ledger rows = %d, %v; want 5", len(all), err)
	}
	for _, f := range all {
		if f.State != want[f.ID] {
			t.Errorf("%s state = %s, want %s", f.ID, f.State, want[f.ID])
		}
		if f.State == FactRejected && f.RejectReason != changeReason {
			t.Errorf("%s reason = %q, want %q", f.ID, f.RejectReason, changeReason)
		}
	}
	rep = e.apply(t, 2, "b/b.go")
	if len(rep.Invalidated) != 0 {
		t.Errorf("second apply invalidated %v, want none", rep.Invalidated)
	}
}

func TestCursorNeverAheadOfEffects(t *testing.T) {
	e := newMaintEnv(t)
	j := e.mt.d.Journal
	e.idx.FailFTS["b/b.go"] = true
	e.write(t, "b/b.go", betaChanged)
	rep := e.apply(t, 1, "a/a.go", "b/b.go")
	if !reflect.DeepEqual(rep.Failed, []string{"b/b.go"}) {
		t.Fatalf("Failed = %v, want [b/b.go]", rep.Failed)
	}
	if _, ok := mustCursor(t, j); ok {
		t.Fatal("cursor advanced although a path failed")
	}
	paths := mustPaths(t, j)
	if _, ok := paths["b/b.go"]; ok {
		t.Fatal("path hash advanced for the failed path")
	}
	if _, ok := paths["a/a.go"]; !ok {
		t.Fatal("the healthy path should have its hash")
	}
	if in := mustIntents(t, j); len(in) != 1 || in[0].Rel != "b/b.go" || in[0].LegsDone&(1<<2) == 0 {
		t.Fatalf("intents = %+v, want b/b.go kept with legs 1-3 done", in)
	}
	delete(e.idx.FailFTS, "b/b.go")
	e.apply(t, 2, "b/b.go")
	cur, ok := mustCursor(t, j)
	if !ok || cur.FSSeq != 2 || cur.Generation < 1 {
		t.Fatalf("cursor = %+v ok=%v, want advanced to seq 2", cur, ok)
	}
	if in := mustIntents(t, j); len(in) != 0 {
		t.Fatalf("intents left: %+v", in)
	}
}

func TestMaintenanceSkipsVectorWhenEmbedderUnconfigured(t *testing.T) {
	e := newMaintEnv(t)
	e.idx.Pending = true
	e.write(t, "b/b.go", betaChanged)
	rep := e.apply(t, 1, "b/b.go")
	if len(rep.Failed) != 0 || !reflect.DeepEqual(rep.VectorPending, []string{"b/b.go"}) || len(rep.Applied) != 1 {
		t.Fatalf("report = %+v, want applied with vector pending", rep)
	}
	paths := mustPaths(t, e.mt.d.Journal)
	if !paths["b/b.go"].VectorPending || len(paths["b/b.go"].ChunkIDs) == 0 {
		t.Fatalf("journal = %+v, want vector_pending with chunk ids", paths["b/b.go"])
	}
	e.idx.Pending, e.idx.FailVectors = false, true
	rep = e.apply(t, 2, "a/a.go")
	if len(rep.Failed) != 0 || !reflect.DeepEqual(rep.VectorPending, []string{"a/a.go"}) {
		t.Fatalf("embedder error report = %+v, want pending, never failed", rep)
	}
}

func TestMaintenanceIdempotent(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", betaChanged)
	first := e.apply(t, 1, "a/a.go", "b/b.go", "c/c.go")
	if len(first.Applied) != 3 {
		t.Fatalf("first report = %+v", first)
	}
	calls := len(e.idx.Calls)
	second := e.apply(t, 1, "c/c.go", "b/b.go", "a/a.go", "b/b.go")
	if len(second.Applied) != 0 || !reflect.DeepEqual(second.Unchanged, []string{"a/a.go", "b/b.go", "c/c.go"}) {
		t.Fatalf("second report = %+v, want all unchanged", second)
	}
	if len(e.idx.Calls) != calls {
		t.Fatalf("second apply made %d IndexLeg calls: %v", len(e.idx.Calls)-calls, e.idx.Calls[calls:])
	}
}

func TestMaintenancePathFailureIsolated(t *testing.T) {
	e := newMaintEnv(t)
	e.idx.FailFTS["b/b.go"] = true
	rep := e.apply(t, 1, "c/c.go", "b/b.go", "a/a.go")
	if !reflect.DeepEqual(rep.Failed, []string{"b/b.go"}) || !reflect.DeepEqual(rep.Applied, []string{"a/a.go", "c/c.go"}) {
		t.Fatalf("report = %+v, want b failed and a, c applied", rep)
	}
}

// TestApplyReplaysHalfDoneIntentFirst is the fail-closed probe: an intent left
// half-done is replayed before a new batch is looked at.
func TestApplyReplaysHalfDoneIntentFirst(t *testing.T) {
	e := newMaintEnv(t)
	ctx := context.Background()
	j := e.mt.d.Journal
	if err := j.beginIntent(ctx, testRepoID, intent{Rel: "a/a.go", Generation: 1, LegsDone: 3}); err != nil {
		t.Fatal(err)
	}
	e.apply(t, 1, "b/b.go")
	paths := mustPaths(t, j)
	if _, ok := paths["a/a.go"]; !ok {
		t.Fatalf("half-done a/a.go was not replayed; paths = %v", paths)
	}
	if in := mustIntents(t, j); len(in) != 0 {
		t.Fatalf("intents left after replay: %+v", in)
	}
	if e.idx.callsFor("fts:a/a.go") != 1 {
		t.Fatalf("calls = %v, want a/a.go replayed exactly once", e.idx.Calls)
	}
}
