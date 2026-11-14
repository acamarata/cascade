package testkit

// Purpose: seed a controller designation under a test data dir, so a fixture
//   that starts a controller daemon resolves the controller role without
//   running init.
// Inputs: a testing.TB and the data dir to seed.
// Outputs: <dataDir>/nodes/controller_designation.json (0600, parent 0700),
//   in the exact shape the production file designation backend reads.
// Constraints: internal/nodes tests import this package, so it cannot import
//   internal/nodes; the record is written by hand and designation_test.go
//   proves the production backend loads it. An existing record is left alone,
//   and a fixture never weakens the role resolver.
// SPORT: build/testkit (ADD).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// designationFile is the designation's path under a data dir.
const designationFile = "controller_designation.json"

// testDesignation mirrors nodes.Designation's JSON field names.
type testDesignation struct {
	InstallationID   string    `json:"installation_id"`
	ControllerNodeID string    `json:"controller_node_id"`
	Epoch            uint64    `json:"epoch"`
	DesignatedAt     time.Time `json:"designated_at"`
	Reason           string    `json:"reason"`
}

// WriteTestDesignation makes dataDir a designated controller home: epoch 1,
// reason init, fixed ids and time. An existing designation is kept.
func WriteTestDesignation(t testing.TB, dataDir string) {
	t.Helper()
	dir := filepath.Join(dataDir, "nodes")
	path := filepath.Join(dir, designationFile)
	if _, err := os.Stat(path); err == nil {
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
	raw, err := json.MarshalIndent(testDesignation{
		InstallationID:   "inst_testkitfixture",
		ControllerNodeID: "node-testkit-fixture",
		Epoch:            1,
		DesignatedAt:     time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
		Reason:           "init",
	}, "", "  ")
	if err != nil {
		t.Fatalf("encode test designation: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := runtime.WriteFileAtomic(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
