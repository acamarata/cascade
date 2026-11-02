package repo

// Purpose: the watch journal's own tests plus the shared pipeline test
//   infrastructure (a persisted IndexLeg double and a real-git, real-sqlite
//   environment) used by maintenance_test.go and maintenance_crash_test.go.
// SPORT: repo/maintenance-journal/ADD test coverage (P1-GEN-04).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memIndex is the IndexLeg double. With a path it persists across processes
// so the crash tests can re-open it after a child exits.
type memIndex struct {
	Path        string              `json:"-"`
	Chunks      map[string][]string `json:"chunks"` // rel -> live chunk ids
	Vectors     map[string]bool     `json:"vectors"`
	Graph       SymbolGraph         `json:"graph"`
	Calls       []string            `json:"calls"`
	Pending     bool                `json:"-"`
	FailFTS     map[string]bool     `json:"-"`
	FailVectors bool                `json:"-"`
}

func newMemIndex(t *testing.T, path string) *memIndex {
	t.Helper()
	x := &memIndex{Path: path, Chunks: map[string][]string{}, Vectors: map[string]bool{}, FailFTS: map[string]bool{}}
	if data, err := os.ReadFile(path); err == nil && path != "" {
		if err := json.Unmarshal(data, x); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func (x *memIndex) save() {
	if x.Path == "" {
		return
	}
	data, err := json.Marshal(x)
	if err == nil {
		err = os.WriteFile(x.Path, data, 0o600)
	}
	if err != nil { // the crash tests trust this file: never lose a write silently
		panic("memIndex save: " + err.Error())
	}
}

func (x *memIndex) WriteFTS(_ context.Context, _, rel string, content []byte) ([]string, error) {
	x.Calls = append(x.Calls, "fts:"+rel)
	if x.FailFTS[rel] {
		return nil, errors.New("fts down")
	}
	var ids []string
	for i, line := range strings.Split(string(content), "\n") {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", rel, i, line)))
		ids = append(ids, hex.EncodeToString(sum[:6]))
	}
	x.Chunks[rel] = ids
	x.save()
	return ids, nil
}

func (x *memIndex) DeleteChunks(_ context.Context, _ string, ids []string) error {
	x.Calls = append(x.Calls, "del:"+strings.Join(ids, ","))
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
		delete(x.Vectors, id)
	}
	for rel, have := range x.Chunks {
		var keep []string
		for _, id := range have {
			if !gone[id] {
				keep = append(keep, id)
			}
		}
		x.Chunks[rel] = keep
	}
	x.save()
	return nil
}

func (x *memIndex) WriteVector(_ context.Context, _, rel string, ids []string, _ []byte) (bool, error) {
	x.Calls = append(x.Calls, "vec:"+rel)
	if x.FailVectors {
		return false, errors.New("embedder down")
	}
	if !x.Pending {
		for _, id := range ids {
			x.Vectors[id] = true
		}
	}
	x.save()
	return x.Pending, nil
}

func (x *memIndex) WriteGraph(_ context.Context, _ string, g SymbolGraph) error {
	x.Calls = append(x.Calls, "graph")
	x.Graph = g
	x.save()
	return nil
}

// callsFor counts recorded calls whose text starts with prefix.
func (x *memIndex) callsFor(prefix string) int {
	n := 0
	for _, c := range x.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// menv is one repository checkout plus its database and pipeline.
type menv struct {
	root, dbPath string
	db           *sql.DB
	idx          *memIndex
	attn         []string
	mt           *Maintenance
	ledger       *LedgerStore
	graph        *GraphStore
}

const testRepoID = "repo-1"

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
}

// newMaintRepo copies testdata/fixture-maint into a fresh git repository.
func newMaintRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	src := filepath.Join("testdata", "fixture-maint")
	err := filepath.Walk(src, func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		rel, _ := filepath.Rel(src, p)
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(root, rel)
		if merr := os.MkdirAll(filepath.Dir(dst), 0o755); merr != nil {
			return merr
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// openEnv opens (or creates) the database and wires the pipeline. seed=true
// also writes the repository row, inventory and a full graph, as a prior
// scan would have.
func openEnv(t *testing.T, root, dbPath, idxPath string, seed bool) *menv {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, clk := context.Background(), newTestMigrateClock()
	for _, apply := range []func() error{
		func() error { return scope.ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "") },
		func() error { return ApplyInventorySchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "") },
		func() error { return ApplyGraphSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "") },
		func() error { return ApplyLedgerSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "") },
		func() error { return ApplyWatchJournalSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "") },
	} {
		if err := apply(); err != nil {
			t.Fatal(err)
		}
	}
	e := &menv{root: root, dbPath: dbPath, db: db, idx: newMemIndex(t, idxPath), ledger: NewLedgerStore(db), graph: NewGraphStore(db)}
	if seed {
		e.seed(t)
	}
	j, err := NewWatchJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	e.mt, err = NewMaintenance(MaintenanceDeps{
		Root: root, RepositoryID: testRepoID, Journal: j, Inventory: NewStore(db), Graph: e.graph, Ledger: e.ledger,
		Index: e.idx, Clock: runtime.NewFixedClock(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
		Attention: func(_ context.Context, ref, text string) error { e.attn = append(e.attn, ref+"|"+text); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *menv) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	seedRepository(t, e.db, testRepoID)
	langs, err := DetectAll(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	inv := Inventory{Repository: RepositoryRef{ID: testRepoID, Remote: "https://example.com/x.git", PathHash: "abc"}, Languages: langs, ScannedAt: 1}
	if err := NewStore(e.db).Upsert(ctx, inv); err != nil {
		t.Fatal(err)
	}
	g, err := goExtractor{}.Extract(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.graph.Upsert(ctx, testRepoID, 1, *g, 1); err != nil {
		t.Fatal(err)
	}
}

func newMaintEnv(t *testing.T) *menv {
	t.Helper()
	root := newMaintRepo(t)
	return openEnv(t, root, filepath.Join(t.TempDir(), "maint.db"), "", true)
}

func (e *menv) write(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, full, content, 0o644)
}

func (e *menv) apply(t *testing.T, seq uint64, rels ...string) BatchReport {
	t.Helper()
	rep, err := e.mt.Apply(context.Background(), Batch{Changed: rels, FSSeq: seq})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

func TestWatchJournalSchemaAndCursor(t *testing.T) {
	e := newMaintEnv(t)
	ctx := context.Background()
	if err := ApplyWatchJournalSchema(ctx, e.db, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	j, _ := NewWatchJournal(e.db)
	if _, ok, err := j.Cursor(ctx, testRepoID); ok || err != nil {
		t.Fatalf("fresh cursor ok=%v err=%v", ok, err)
	}
	if err := j.AdvanceCursor(ctx, Cursor{RepositoryID: testRepoID, FSSeq: 9, Commit: "c1", TreeOID: "t1", Generation: 3}); err != nil {
		t.Fatal(err)
	}
	err := j.AdvanceCursor(ctx, Cursor{RepositoryID: testRepoID, Generation: 2})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindConflict || !strings.Contains(ce.Error(), "below the stored generation") {
		t.Fatalf("lower generation error = %v, want KindConflict below the stored generation", err)
	}
	got, ok, err := j.Cursor(ctx, testRepoID)
	if err != nil || !ok || got.Generation != 3 || got.FSSeq != 9 || got.Commit != "c1" {
		t.Fatalf("cursor = %+v, want the generation-3 row untouched", got)
	}
	if _, err := NewWatchJournal(nil); err == nil {
		t.Fatal("NewWatchJournal(nil) must fail")
	}
}
