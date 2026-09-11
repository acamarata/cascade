// Purpose: the real-tree and idempotency proofs for ComputeRegistry and
//
//	RenderRegistry, matching internal/inventory/tree_test.go's precedent.
//
// SPORT: internal.inventory.sport.ComputeRegistry/ADDED (tests).
package sport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// sportModuleRoot resolves the repo root by walking up from this file.
func sportModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("sport: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("sport: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestComputeRegistry_RealTreeNoMalformed proves the real, tracked tree
// has zero unparseable SPORT lines under this package's grammar. This is
// the gate's real-tree half at the package level; internal/build's
// sportgate.go additionally wires this into the gate suite.
func TestComputeRegistry_RealTreeNoMalformed(t *testing.T) {
	root := sportModuleRoot(t)
	if _, err := ComputeRegistry(root, "t"); err != nil {
		t.Fatalf("real tree has a malformed SPORT line: %v", err)
	}
}

// TestRenderRegistry_Idempotent proves two RenderRegistry calls against an
// unchanged tree with a fixed clock produce byte-identical output.
func TestRenderRegistry_Idempotent(t *testing.T) {
	root := sportModuleRoot(t)
	clock := cascaderuntime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	a, err := RenderRegistry(root, clock)
	if err != nil {
		t.Fatalf("RenderRegistry #1: %v", err)
	}
	b, err := RenderRegistry(root, clock)
	if err != nil {
		t.Fatalf("RenderRegistry #2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("RenderRegistry is not idempotent:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

// TestRegistryJSON_MatchesTrackedArtifact proves the tracked registry.json
// this package embeds agrees with a fresh ComputeRegistry call against the
// current tree — the same drift internal/build's sportgate.go also
// checks in CI, kept here too so `go test ./internal/inventory/sport/...`
// alone catches a forgotten `go run ./internal/inventory/gen`.
func TestRegistryJSON_MatchesTrackedArtifact(t *testing.T) {
	root := sportModuleRoot(t)
	tracked, err := os.ReadFile(filepath.Join(root, "internal", "inventory", "sport", "registry.json"))
	if err != nil {
		t.Fatalf("read tracked registry.json: %v", err)
	}
	var trackedReg Registry
	if err := json.Unmarshal(tracked, &trackedReg); err != nil {
		t.Fatalf("parse tracked registry.json: %v", err)
	}
	fresh, err := ComputeRegistry(root, trackedReg.GeneratedAt)
	if err != nil {
		t.Fatalf("ComputeRegistry: %v", err)
	}
	if len(fresh.Entities) != len(trackedReg.Entities) || fresh.TotalSites != trackedReg.TotalSites {
		t.Fatalf("registry.json is stale: tracked has %d entities/%d sites, tree has %d/%d — run `go run ./internal/inventory/gen`",
			len(trackedReg.Entities), trackedReg.TotalSites, len(fresh.Entities), fresh.TotalSites)
	}
}
