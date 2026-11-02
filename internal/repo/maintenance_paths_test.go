package repo

// Purpose: error and edge paths of the maintenance pipeline: dependency
//   validation, a deleted path, repo-level rescans, a language family that
//   disappears, a failed replay holding the cursor, a canceled context.
// SPORT: repo/maintenance-pipeline/ADD test coverage (P1-GEN-04).

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNewMaintenanceRejectsEachMissingDep(t *testing.T) {
	e := newMaintEnv(t)
	cases := map[string]func(d *MaintenanceDeps){
		"root":      func(d *MaintenanceDeps) { d.Root = "" },
		"repo id":   func(d *MaintenanceDeps) { d.RepositoryID = "" },
		"journal":   func(d *MaintenanceDeps) { d.Journal = nil },
		"inventory": func(d *MaintenanceDeps) { d.Inventory = nil },
		"graph":     func(d *MaintenanceDeps) { d.Graph = nil },
		"ledger":    func(d *MaintenanceDeps) { d.Ledger = nil },
		"index":     func(d *MaintenanceDeps) { d.Index = nil },
		"attention": func(d *MaintenanceDeps) { d.Attention = nil },
		"clock":     func(d *MaintenanceDeps) { d.Clock = nil },
	}
	for name, mutate := range cases {
		d := e.mt.d
		mutate(&d)
		m, err := NewMaintenance(d)
		if m != nil {
			t.Errorf("%s: got a pipeline with a missing dependency", name)
		}
		expectKind(t, name, err, cascade.KindInvalidInput)
	}
}

// inventory reads the stored inventory of the test repository.
func (e *menv) inventory(t *testing.T) Inventory {
	t.Helper()
	inv, ok, err := e.mt.d.Inventory.Get(context.Background(), testRepoID)
	if err != nil || !ok {
		t.Fatalf("inventory ok=%v err=%v", ok, err)
	}
	return inv
}

func TestMaintenanceDeletedPathDeindexes(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", betaChanged)
	e.apply(t, 1, "b/b.go")
	if len(e.idx.Chunks["b/b.go"]) == 0 {
		t.Fatal("setup: b/b.go has no chunks")
	}
	if err := os.Remove(filepath.Join(e.root, "b", "b.go")); err != nil {
		t.Fatal(err)
	}
	rep := e.apply(t, 2, "b/b.go")
	if !reflect.DeepEqual(rep.Applied, []string{"b/b.go"}) || len(rep.Refused)+len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want the deletion applied", rep)
	}
	if len(e.idx.Chunks["b/b.go"]) != 0 || e.idx.callsFor("fts:b/b.go") != 1 {
		t.Fatalf("deleted path still indexed: chunks %v calls %v", e.idx.Chunks["b/b.go"], e.idx.Calls)
	}
	if _, ok := mustPaths(t, e.mt.d.Journal)["b/b.go"]; ok {
		t.Fatal("journal kept the deleted path")
	}
	g, _, _, err := e.graph.Get(context.Background(), testRepoID)
	if err != nil || strings.Contains(packageNodesJSON(t, g, "fixturemaint/b"), "Beta") {
		t.Fatalf("package b survived its deletion (err %v)", err)
	}
	if cur, ok := mustCursor(t, e.mt.d.Journal); !ok || cur.FSSeq != 2 {
		t.Fatalf("cursor = %+v ok=%v, want seq 2", cur, ok)
	}
}

func TestMaintenanceRescansRepoLevelFacts(t *testing.T) {
	e := newMaintEnv(t)
	if inv := e.inventory(t); inv.Harness.ClaudeMD || inv.Harness.AgentsMD || inv.Layout.FileCount != 0 {
		t.Fatalf("setup: fixture already has harness or layout facts: %+v", inv)
	}
	e.write(t, "CLAUDE.md", "# notes\n")
	e.write(t, "docs/AGENTS.md", "# agents\n")
	rep := e.apply(t, 1, "CLAUDE.md", "docs/AGENTS.md")
	if len(rep.Applied) != 2 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want both applied", rep)
	}
	inv := e.inventory(t)
	if !inv.Harness.ClaudeMD || inv.Layout.FileCount == 0 {
		t.Fatalf("inventory harness %+v layout %+v, want both rescanned", inv.Harness, inv.Layout)
	}
	if inv.ScannedAt == 1 {
		t.Fatal("a moved fact must restamp ScannedAt from the injected clock")
	}
}

func TestMaintenanceDroppedFamilyFallsBackToGeneric(t *testing.T) {
	e := newMaintEnv(t)
	var fams []Language
	onDetect = func(l Language) { fams = append(fams, l) }
	t.Cleanup(func() { onDetect = func(Language) {} })
	if err := os.Remove(filepath.Join(e.root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	rep := e.apply(t, 1, "go.mod")
	if !reflect.DeepEqual(rep.Applied, []string{"go.mod"}) || !reflect.DeepEqual(fams, []Language{LanguageGo}) {
		t.Fatalf("report = %+v families %v, want go.mod applied rerunning only go", rep, fams)
	}
	langs := e.inventory(t).Languages
	if len(langs) != 1 || langs[0].Language != LanguageGeneric {
		t.Fatalf("languages = %+v, want only the generic fallback", langs)
	}
}

func TestMaintenanceFailedReplayHoldsCursor(t *testing.T) {
	e := newMaintEnv(t)
	e.idx.FailFTS["b/b.go"] = true
	e.write(t, "b/b.go", betaChanged)
	e.apply(t, 1, "b/b.go")
	rep := e.apply(t, 2, "a/a.go")
	if !reflect.DeepEqual(rep.Applied, []string{"a/a.go"}) {
		t.Fatalf("report = %+v, want a/a.go applied", rep)
	}
	if _, ok := mustCursor(t, e.mt.d.Journal); ok {
		t.Fatal("cursor advanced while a replayed intent still fails")
	}
	if in := mustIntents(t, e.mt.d.Journal); len(in) != 1 || in[0].Rel != "b/b.go" {
		t.Fatalf("intents = %+v, want b/b.go kept", in)
	}
}

func TestMaintenanceCanceledContextAppliesNothing(t *testing.T) {
	e := newMaintEnv(t)
	e.write(t, "b/b.go", betaChanged)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.mt.Apply(ctx, Batch{Changed: []string{"b/b.go"}, FSSeq: 1}); err == nil {
		t.Fatal("Apply with a canceled context must fail")
	}
	if len(e.idx.Calls) != 0 || len(mustPaths(t, e.mt.d.Journal)) != 0 {
		t.Fatalf("canceled Apply had effects: calls %v", e.idx.Calls)
	}
	if _, ok := mustCursor(t, e.mt.d.Journal); ok {
		t.Fatal("canceled Apply advanced the cursor")
	}
}
