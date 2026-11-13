package main

// Purpose: seed the controller designation the cmd/cascade daemon fixtures
//   need, through the production file designation backend.
// Constraints: no build tag (fakeDaemonPaths, its one shared caller, is
//   untagged). A fixture never weakens the role resolver: homes that need a
//   role conflict remove the record with removeTestDesignation.
// SPORT: cmd/cascade test-support (ADD).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
)

// testDesignation is the fixed controller designation fixtures seed.
func testDesignation() nodes.Designation {
	return nodes.Designation{
		InstallationID:   "inst_cmdcascadetestfixture",
		ControllerNodeID: "node-cmd-cascade-test",
		Epoch:            1,
		DesignatedAt:     time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
		Reason:           "init",
	}
}

// designationSeeds records the data dirs whose fixture designation is
// settled (seeded, written or removed by a test), so DataDir never re-seeds.
var designationSeeds = struct {
	sync.Mutex
	done map[string]bool
}{done: map[string]bool{}}

// seedFixtureDesignation seeds dataDir once, and only after it exists.
func seedFixtureDesignation(dataDir string) {
	designationSeeds.Lock()
	defer designationSeeds.Unlock()
	if designationSeeds.done[dataDir] {
		return
	}
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		return
	}
	designationSeeds.done[dataDir] = true
	if err := seedTestDesignation(dataDir); err != nil {
		panic("fakeDaemonPaths: seed controller designation: " + err.Error())
	}
}

// settleDesignation marks dataDir so the fixture seed leaves it alone.
func settleDesignation(dataDir string) {
	designationSeeds.Lock()
	defer designationSeeds.Unlock()
	designationSeeds.done[dataDir] = true
}

// seedTestDesignation writes testDesignation under dataDir unless a
// designation already exists there.
func seedTestDesignation(dataDir string) error {
	backend := nodes.NewFileDesignationBackend(dataDir)
	if _, ok, err := backend.Load(); err != nil || ok {
		return err
	}
	return backend.Write(testDesignation())
}

// writeTestDesignation makes dataDir a designated controller home.
func writeTestDesignation(t *testing.T, dataDir string) {
	t.Helper()
	settleDesignation(dataDir)
	if err := seedTestDesignation(dataDir); err != nil {
		t.Fatalf("seed controller designation under %s: %v", dataDir, err)
	}
}

// removeTestDesignation drops the designation under dataDir, for role tests
// that need a home with no designation.
func removeTestDesignation(t *testing.T, dataDir string) {
	t.Helper()
	settleDesignation(dataDir)
	if err := nodes.NewFileDesignationBackend(dataDir).Remove(); err != nil {
		t.Fatalf("remove controller designation under %s: %v", dataDir, err)
	}
}

// writeTestBinding writes b as the data dir's controller binding file.
func writeTestBinding(t *testing.T, dataDir string, b nodes.ControllerBinding) {
	t.Helper()
	path := filepath.Join(dataDir, "nodes", "controller_binding.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir binding dir: %v", err)
	}
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write binding: %v", err)
	}
}

// removeTestFile removes path; an absent file is fine.
func removeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", path, err)
	}
}
