//go:build windows

// Purpose: acceptance [8] on a real windows host: no unit items, the file
//
//	vault entries are purged by a default uninstall, and --purge-data
//	refuses before the inventory.
package main

import (
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestUninstallWindowsHasNoUnitsAndRefusesPurge(t *testing.T) {
	f := newUninstallFixture(t)
	f.goos = "windows"
	out, err := execUninstall(t, f.deps(nil, nil), "--yes", "--json")
	if err != nil {
		t.Fatalf("windows uninstall: %v (%s)", err, out)
	}
	if strings.Contains(out, "daemon-unit") || strings.Contains(out, "widget-agent") {
		t.Fatalf("windows inventory listed a unit: %s", out)
	}
	if names, _ := f.custody.List(t.Context()); len(names) != 0 {
		t.Fatalf("file vault entries left: %v", names)
	}
	if _, err := os.Stat(f.unit); err != nil {
		t.Fatalf("a windows uninstall touched the unit path: %v", err)
	}
	invs := f.invs
	_, err = execUninstall(t, f.deps(nil, nil), "--purge-data", "--yes")
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindUnsupported || f.invs != invs {
		t.Fatalf("windows --purge-data = %v (inventories %d -> %d); want KindUnsupported first", err, invs, f.invs)
	}
	if _, err := os.Stat(f.paths.DataDir()); err != nil {
		t.Fatalf("data dir after a refused purge: %v", err)
	}
}
