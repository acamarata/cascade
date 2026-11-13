package main

// Purpose: seed the controller designation the cmd/cascade daemon fixtures
//   need, through the production file designation backend.
// Constraints: no build tag (fakeDaemonPaths and fakeMemoryPaths, its
//   shared callers, are untagged). A fixture never weakens the role
//   resolver: homes that need a role conflict remove the record with
//   removeTestDesignation.
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

// fixtureDataDir is root's data dir for the fake path providers
// (fakeDaemonPaths, fakeMemoryPaths), seeded once it exists.
func fixtureDataDir(root string) string {
	dir := filepath.Join(root, "data")
	if root != "" {
		seedFixtureDesignation(dir)
	}
	return dir
}

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

// testControllerKeyB64 is a well-formed pinned controller key (32 zero bytes).
const testControllerKeyB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// fakeDesignations is an in-memory designation backend.
type fakeDesignations struct {
	d   nodes.Designation
	ok  bool
	err error
}

func (f fakeDesignations) Load() (nodes.Designation, bool, error) { return f.d, f.ok, f.err }
func (fakeDesignations) Write(nodes.Designation) error            { return nil }
func (fakeDesignations) Remove() error                            { return nil }

// fakeBinding is a resolver input for the store half of the eligibility
// check: a complete binding (ok) or a load error, and no designation.
type fakeBinding struct {
	ok  bool
	err error
}

func (f fakeBinding) Load() (nodes.ControllerBinding, bool, error) {
	return nodes.ControllerBinding{Endpoint: "tcp://controller.invalid:7443", ControllerPubKeyB64: testControllerKeyB64}, f.ok, f.err
}

// DesignationBackend exposes an empty designation, so ok resolves as node.
func (fakeBinding) DesignationBackend() nodes.DesignationBackend { return fakeDesignations{} }

// seedRunHomeDesignation designates the shared per-run HOME's data dir as a
// controller for one test, and removes it afterwards if this test wrote it.
func seedRunHomeDesignation(t *testing.T, dataDir string) {
	t.Helper()
	if _, ok, err := nodes.NewFileDesignationBackend(dataDir).Load(); err != nil || ok {
		if err != nil {
			t.Fatalf("load designation under %s: %v", dataDir, err)
		}
		return
	}
	writeTestDesignation(t, dataDir)
	t.Cleanup(func() { _ = nodes.NewFileDesignationBackend(dataDir).Remove() })
}
