package uninstall

// Purpose: the windows inventory shape (acceptance [8], portable half),
//   and the unit items' error paths.

import (
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWindowsInventoryHasNoUnitsAndRefusesData(t *testing.T) {
	f := newVaultFixture(t, "windows")
	items := mustInventory(t, f.deps)
	for _, it := range items {
		if it.Kind() == KindDaemonUnit || it.Kind() == KindWidgetAgent {
			t.Fatalf("windows inventory has a unit: %s", it.Locator())
		}
	}
	rep, err := Run(t.Context(), items, Options{PurgeData: true})
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindUnsupported {
		t.Fatalf("windows purge = %v; want KindUnsupported", err)
	}
	if !exists(f.paths.DataDir()) || !exists(f.paths.ConfigPath()) {
		t.Fatal("windows purge removed data")
	}
	if left, _ := f.custody.List(t.Context()); len(left) != 0 {
		t.Fatalf("windows file vault entries left: %v", left)
	}
	running := filepath.Join(f.installDir, "cascade.exe")
	if d := deltaFor(t, rep, running); d.Action != ActionKept || !exists(running) {
		t.Fatalf("running windows executable = %+v", d)
	}
}

// failingUnits fails every Uninstall.
type failingUnits struct{}

func (failingUnits) Install(service.Config) (service.DeltaReport, error) {
	return service.DeltaReport{}, nil
}
func (failingUnits) Uninstall(service.Config) (service.DeltaReport, error) {
	return service.DeltaReport{}, cascade.New(cascade.KindConflict, "fake: foreign unit")
}

func TestUnitItemEdges(t *testing.T) {
	f := newFixture(t, "linux")
	f.deps.Services = failingUnits{}
	items := unitItems(f.deps)
	if len(items) != 1 {
		t.Fatalf("linux units = %d; want the daemon unit only", len(items))
	}
	if d, err := items[0].Remove(t.Context(), false); err == nil || d.Action != ActionKept {
		t.Fatalf("failing unit = %+v, %v", d, err)
	}
	f.deps.DaemonCfg = &service.Config{HomeDir: "relative-home"}
	if d, err := unitItems(f.deps)[0].Remove(t.Context(), true); err == nil || d.Action != ActionKept {
		t.Fatalf("relative unit locator = %+v, %v", d, err)
	}
}
