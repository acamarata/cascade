package repo

// Purpose: crash-safety, admission-first and boundary tests for the
//   maintenance pipeline. The crash matrix re-executes this test binary as a
//   child that exits 3 right after a chosen leg (the P1-SEC-33 recipe shape).
// SPORT: repo/maintenance-pipeline/ADD test coverage (P1-GEN-04).

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// crashChild is the re-exec entry: run one Apply, dying after leg N.
func TestMaintenanceCrashChild(t *testing.T) {
	leg, err := strconv.Atoi(os.Getenv("CASCADE_TEST_CRASH_LEG"))
	if err != nil {
		t.Skip("crash child only runs when re-executed by TestMaintenanceAtomicCommit")
	}
	e := openEnv(t, os.Getenv("CASCADE_TEST_CRASH_ROOT"), os.Getenv("CASCADE_TEST_CRASH_DB"), os.Getenv("CASCADE_TEST_CRASH_IDX"), false)
	afterLeg = func(_ string, n int) {
		if n == leg {
			os.Exit(3)
		}
	}
	_, _ = e.mt.Apply(context.Background(), Batch{Changed: []string{"b/b.go"}, FSSeq: 7})
	os.Exit(0)
}

func TestMaintenanceAtomicCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("crash re-exec relies on POSIX process semantics in CI")
	}
	for leg := 1; leg <= 6; leg++ {
		t.Run("after_leg_"+strconv.Itoa(leg), func(t *testing.T) { crashAfterLeg(t, leg) })
	}
}

func crashAfterLeg(t *testing.T, leg int) {
	root := newMaintRepo(t)
	dir := t.TempDir()
	db, idx := filepath.Join(dir, "maint.db"), filepath.Join(dir, "idx.json")
	seeded := openEnv(t, root, db, idx, true)
	seedCitingFact(t, seeded)
	_ = seeded.db.Close()
	if err := os.WriteFile(filepath.Join(root, "b", "b.go"), []byte(betaChanged), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMaintenanceCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CASCADE_TEST_CRASH_LEG="+strconv.Itoa(leg), "CASCADE_TEST_CRASH_ROOT="+root,
		"CASCADE_TEST_CRASH_DB="+db, "CASCADE_TEST_CRASH_IDX="+idx)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child err = %v, want exit 3\n%s", err, out)
	}
	e := openEnv(t, root, db, idx, false)
	ctx := context.Background()
	j := e.mt.d.Journal
	in := mustIntents(t, j)
	if len(in) != 1 || in[0].Rel != "b/b.go" || in[0].LegsDone != (1<<leg)-1 {
		t.Fatalf("after crash intents = %+v, want b/b.go with legs 1..%d done", in, leg)
	}
	if _, ok := mustCursor(t, j); ok {
		t.Fatal("cursor is ahead of the crashed batch")
	}
	rep, err := e.mt.Replay(ctx)
	if err != nil || !reflect.DeepEqual(rep.Applied, []string{"b/b.go"}) {
		t.Fatalf("Replay = %+v, %v", rep, err)
	}
	assertConverged(t, e)
}

// seedCitingFact adds one accepted ledger row citing b/b.go, which the
// replayed ledger leg must reject.
func seedCitingFact(t *testing.T, e *menv) {
	t.Helper()
	f := InferredFact{ID: "fb", RepositoryID: testRepoID, Subject: SubjectLanguages, Fact: "go",
		Source: "scan of b/b.go", Confidence: 0.5, Version: 1, State: FactAccepted, AcceptedBy: "go-detector"}
	if err := e.ledger.Append(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

// assertConverged checks the journal, index and graph agree after Replay.
func assertConverged(t *testing.T, e *menv) {
	t.Helper()
	ctx := context.Background()
	paths := mustPaths(t, e.mt.d.Journal)
	ps := paths["b/b.go"]
	if ps.VectorPending || len(ps.ChunkIDs) == 0 || !reflect.DeepEqual(e.idx.Chunks["b/b.go"], ps.ChunkIDs) {
		t.Fatalf("journal %+v and index chunks %v disagree", ps, e.idx.Chunks["b/b.go"])
	}
	for _, id := range ps.ChunkIDs {
		if !e.idx.Vectors[id] {
			t.Errorf("chunk %s has no vector", id)
		}
	}
	g, _, _, err := e.graph.Get(ctx, testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packageNodesJSON(t, g, "fixturemaint/b"), "Extra") || !reflect.DeepEqual(e.idx.Graph, g) {
		t.Errorf("graph not converged: %s", packageNodesJSON(t, g, "fixturemaint/b"))
	}
	if in := mustIntents(t, e.mt.d.Journal); len(in) != 0 {
		t.Errorf("intents after replay: %+v", in)
	}
	rows, err := e.ledger.List(ctx, LedgerFilter{RepositoryID: testRepoID})
	if err != nil || len(rows) != 1 || rows[0].State != FactRejected || rows[0].RejectReason != changeReason {
		t.Errorf("ledger after replay = %+v, %v; want the b/b.go fact rejected", rows, err)
	}
	if cur, ok := mustCursor(t, e.mt.d.Journal); !ok || cur.Generation != 1 {
		t.Errorf("cursor = %+v ok=%v, want generation 1", cur, ok)
	}
}

func TestMaintenanceRefusesSecretFile(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", betaChanged)
	e.apply(t, 1, "b/b.go")
	old := e.idx.Chunks["b/b.go"]
	e.write(t, "b/b.go", "package b\n\nvar Key = \""+canary()+"\"\n")
	before := len(e.idx.Calls)
	rep := e.apply(t, 2, "b/b.go")
	if !reflect.DeepEqual(rep.Refused, []string{"b/b.go"}) || len(rep.Applied) != 0 {
		t.Fatalf("report = %+v, want b/b.go refused", rep)
	}
	for _, c := range e.idx.Calls[before:] {
		if !strings.HasPrefix(c, "del:") {
			t.Errorf("refused path reached the index: %q", c)
		}
	}
	if len(e.idx.Chunks["b/b.go"]) != 0 || len(old) == 0 {
		t.Errorf("old chunks %v not deindexed (left %v)", old, e.idx.Chunks["b/b.go"])
	}
	if p := mustPaths(t, e.mt.d.Journal); len(p["b/b.go"].ChunkIDs) != 0 || p["b/b.go"].ContentSHA256 != "" {
		t.Errorf("journal still holds the refused path: %+v", p["b/b.go"])
	}
}

func TestMaintenanceIngestRefusalAttention(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", "package b\n\nvar Key = \""+canary()+"\"\n")
	rep := e.apply(t, 1, "a/a.go", "b/b.go")
	if len(e.attn) != 1 || !strings.HasPrefix(e.attn[0], "repo.ingest:repo-1:b/b.go|") || strings.Contains(e.attn[0], canary()) {
		t.Fatalf("attention = %v, want exactly one call for b/b.go carrying no secret", e.attn)
	}
	if !reflect.DeepEqual(rep.Refused, []string{"b/b.go"}) || !reflect.DeepEqual(rep.Applied, []string{"a/a.go"}) {
		t.Fatalf("report = %+v", rep)
	}
	_, err := NewMaintenance(MaintenanceDeps{Root: e.root, RepositoryID: testRepoID, Journal: e.mt.d.Journal,
		Inventory: e.mt.d.Inventory, Graph: e.graph, Ledger: e.ledger, Index: e.idx, Clock: e.mt.d.Clock})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput || !strings.Contains(ce.Error(), "Attention") {
		t.Fatalf("nil Attention error = %v, want KindInvalidInput naming Attention", err)
	}
}

func TestRepoImportsNoRetrieval(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	var got []string
	for _, p := range strings.Fields(string(out)) {
		p = strings.TrimPrefix(p, "github.com/acamarata/cascade/")
		for _, bad := range []string{"internal/retrieval", "internal/fleet", "internal/daemon"} {
			if p == bad || strings.HasPrefix(p, bad+"/") {
				got = append(got, p)
			}
		}
	}
	if want := []string{"internal/retrieval/corpus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forbidden deps = %v, want exactly %v (reached through internal/secrets)", got, want)
	}
}

func TestMaintenanceUsesInjectedIndexLeg(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", betaChanged)
	e.apply(t, 1, "b/b.go")
	if e.idx.callsFor("fts:") != 1 || e.idx.callsFor("vec:") != 1 || e.idx.callsFor("graph") != 1 {
		t.Fatalf("calls = %v, want one fts, vec and graph write through the injected leg", e.idx.Calls)
	}
	files, err := filepath.Glob("maintenance*.go")
	if err != nil || len(files) < 2 {
		t.Fatalf("glob = %v, %v; want the maintenance sources", files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			if strings.Contains(im.Path.Value, "retrieval") {
				t.Errorf("%s imports %s", f, im.Path.Value)
			}
		}
	}
}

func TestMaintenanceNoReadBeforeAdmit(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "big.txt", strings.Repeat("x", 2<<20))
	rels := []string{"big.txt"}
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("mkfifo", filepath.Join(e.root, "pipe.txt")).CombinedOutput(); err != nil {
			t.Skipf("mkfifo unavailable: %v %s", err, out)
		}
		rels = append(rels, "pipe.txt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := e.mt.Apply(ctx, Batch{Changed: rels, FSSeq: 1})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Refused) != len(rels) || len(rep.Applied) != 0 {
		t.Fatalf("report = %+v, want %v refused", rep, rels)
	}
	if e.idx.callsFor("fts:") != 0 || e.idx.callsFor("vec:") != 0 {
		t.Fatalf("refused paths reached the index: %v", e.idx.Calls)
	}
}
