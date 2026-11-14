package testkit_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestWriteTestDesignationIsLoadedByTheProductionBackend(t *testing.T) {
	dataDir := t.TempDir()
	testkit.WriteTestDesignation(t, dataDir)
	d, found, err := nodes.NewFileDesignationBackend(dataDir).Load()
	if err != nil || !found {
		t.Fatalf("production backend Load: found=%v err=%v; want the seeded record", found, err)
	}
	if d.Epoch != 1 || d.Reason != "init" {
		t.Fatalf("designation %+v; want epoch 1, reason init", d)
	}
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(dataDir))
	if err != nil || role != nodes.RoleController {
		t.Fatalf("role over the seeded home = %q, %v; want controller", role, err)
	}
}

func TestWriteTestDesignationModesAndIdempotence(t *testing.T) {
	dataDir := t.TempDir()
	testkit.WriteTestDesignation(t, dataDir)
	path := filepath.Join(dataDir, "nodes", "controller_designation.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	testkit.WriteTestDesignation(t, dataDir)
	second, err := os.ReadFile(path)
	if err != nil || string(first) != string(second) {
		t.Fatalf("second call changed the record (err %v)", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, err %v; want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v, err %v; want 0700", info, err)
	}
}

func TestWriteTestDesignationKeepsAnExistingRecord(t *testing.T) {
	dataDir := t.TempDir()
	backend := nodes.NewFileDesignationBackend(dataDir)
	seeded := nodes.Designation{
		InstallationID: "inst_other", ControllerNodeID: "node-other", Epoch: 7,
		DesignatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Reason: "handover",
	}
	if err := backend.Write(seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}
	testkit.WriteTestDesignation(t, dataDir)
	got, _, err := backend.Load()
	if err != nil || got.Epoch != 7 || got.InstallationID != "inst_other" {
		t.Fatalf("existing designation overwritten: %+v err %v", got, err)
	}
}
