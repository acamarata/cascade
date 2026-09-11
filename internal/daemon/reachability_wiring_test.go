package daemon

// Purpose: unit coverage for WireReachability's two documented outcomes:
//   the no-stored-graph Skipped state (today's real production posture,
//   per the ticket journal), and the wired state once a graph is really
//   stored - proven through the same cascade.db path WireReachability
//   itself opens, never a fabricated graph handed to it directly.
// SPORT: internal/daemon (ADD, coverage-floor fix).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestWireReachability_NoStoredGraph_Skipped proves the honest, disclosed
// degradation: with no scanner having written a SymbolGraph yet,
// WireReachability reports the "jobs.reachability" subsystem Skipped,
// never a fabricated graph.
func TestWireReachability_NoStoredGraph_Skipped(t *testing.T) {
	paths := fakePathsFor(t, "")
	manifest := NewManifest(nil, runtime.NewSystemClock())

	if err := WireReachability(context.Background(), manifest, paths, runtime.NewSystemClock()); err != nil {
		t.Fatalf("WireReachability: unexpected error %v", err)
	}

	found := false
	for _, s := range manifest.Snapshot() {
		if s.Name == reachabilitySubsystem {
			found = true
			if s.State != SubsystemSkipped {
				t.Errorf("jobs.reachability state = %v, want SubsystemSkipped", s.State)
			}
		}
	}
	if !found {
		t.Fatal("jobs.reachability subsystem never registered")
	}
}

// TestWireReachability_StoredGraph_Wired proves the opposite branch: once
// a real SymbolGraph is stored for this repository (written through the
// same cascade.db WireReachability itself opens and migrates), a second
// call wires a real jobs.ReachabilityFn and reports the subsystem
// Started/Running, never Skipped.
func TestWireReachability_StoredGraph_Wired(t *testing.T) {
	paths := fakePathsFor(t, "")
	clock := runtime.NewSystemClock()

	// First call creates the data dir and applies the graph schema
	// (idempotent) with nothing stored yet - mirrors production startup
	// order, and gives us a real, already-migrated cascade.db to write
	// a graph into.
	preManifest := NewManifest(nil, clock)
	if err := WireReachability(context.Background(), preManifest, paths, clock); err != nil {
		t.Fatalf("WireReachability (pre): unexpected error %v", err)
	}

	repositoryID := gitRootExec(context.Background(), paths.DataDir())
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open cascade.db: %v", err)
	}
	graph := repo.SymbolGraph{Nodes: []repo.GraphNode{{ID: "pkg/example", Kind: repo.NodePackage, Package: "pkg/example"}}}
	if err := repo.NewGraphStore(db).Upsert(context.Background(), repositoryID, 1, graph, clock.Now().Unix()); err != nil {
		_ = db.Close()
		t.Fatalf("Upsert graph: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close cascade.db: %v", err)
	}

	manifest := NewManifest(nil, clock)
	if err := WireReachability(context.Background(), manifest, paths, clock); err != nil {
		t.Fatalf("WireReachability (post): unexpected error %v", err)
	}

	found := false
	for _, s := range manifest.Snapshot() {
		if s.Name == reachabilitySubsystem {
			found = true
			if s.State == SubsystemSkipped {
				t.Errorf("jobs.reachability state = %v, want wired (not Skipped) now that a graph is stored", s.State)
			}
		}
	}
	if !found {
		t.Fatal("jobs.reachability subsystem never registered")
	}
}
