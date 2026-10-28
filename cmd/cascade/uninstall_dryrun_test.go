// Purpose: the per-OS contract of `cascade uninstall --dry-run --purge-data`
// over the production inventory, shared by TestUninstallDryRunNeverWritesTheRealStore.
//
// Contract: the inventory is built with the host GOOS (mount_uninstall.go).
// On posix the data items are removable, so the dry run succeeds and plans
// the data dir. On windows internal/uninstall/data.go keeps all three data
// items (elevation key, config, data dir) with the note "--purge-data is not
// supported on windows" and Run reports KindUnsupported: the report still
// prints, the command fails, and nothing is written.
package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// windowsPurgeNote is the note internal/uninstall/data.go puts on a windows data item.
const windowsPurgeNote = "--purge-data is not supported on windows"

// assertPurgeDryRun fails t for every way the report and error of a
// --dry-run --purge-data run depart from goos's contract; cascadeHome is the
// fake CASCADE_HOME.
func assertPurgeDryRun(t *testing.T, goos, out string, err error, cascadeHome string) {
	t.Helper()
	for _, p := range purgeDryRunProblems(goos, out, err, cascadeHome) {
		t.Error(p)
	}
}

// purgeDryRunProblems lists the departures from goos's contract (none = met).
func purgeDryRunProblems(goos, out string, err error, cascadeHome string) []string {
	if goos != "windows" {
		if err != nil || !strings.Contains(out, "data-dir") {
			return []string{fmt.Sprintf("dry run = %q, %v", out, err)}
		}
		return nil
	}
	var problems []string
	for _, it := range []struct{ kind, path string }{
		{"elevation-key", filepath.Join(cascadeHome, "data", "elevation.key")},
		{"config", filepath.Join(cascadeHome, "config.toml")},
		{"data-dir", filepath.Join(cascadeHome, "data")},
	} {
		want := fmt.Sprintf("%-12s %-15s %s (%s)", "kept", it.kind, it.path, windowsPurgeNote)
		if !hasLine(out, want) {
			problems = append(problems, fmt.Sprintf("windows dry run lacks the line %q in %q", want, out))
		}
	}
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindUnsupported || !strings.Contains(err.Error(), windowsPurgeNote) {
		problems = append(problems, fmt.Sprintf("windows dry run error = %v; want KindUnsupported naming %q", err, windowsPurgeNote))
	}
	if strings.Contains(out, "would-remove data-dir") || strings.Contains(out, "would-remove config") {
		problems = append(problems, fmt.Sprintf("windows dry run plans a data removal: %q", out))
	}
	return problems
}

// hasLine reports whether out has want as one whole line.
func hasLine(out, want string) bool {
	for _, l := range strings.Split(out, "\n") {
		if l == want {
			return true
		}
	}
	return false
}

// TestAssertPurgeDryRunHoldsBothContracts feeds each OS's real output shape to
// the checker, so the posix branch cannot silently accept the windows output
// (and the reverse) on any host.
func TestAssertPurgeDryRunHoldsBothContracts(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".cascade")
	posix := "would-remove data-dir        " + filepath.Join(home, "data")
	winOut := strings.Join([]string{
		fmt.Sprintf("%-12s %-15s %s (%s)", "kept", "elevation-key", filepath.Join(home, "data", "elevation.key"), windowsPurgeNote),
		fmt.Sprintf("%-12s %-15s %s (%s)", "kept", "config", filepath.Join(home, "config.toml"), windowsPurgeNote),
		fmt.Sprintf("%-12s %-15s %s (%s)", "kept", "data-dir", filepath.Join(home, "data"), windowsPurgeNote),
	}, "\n") + "\n"
	unsupported := cascade.New(cascade.KindUnsupported, "uninstall: some items were not removed: "+windowsPurgeNote)

	if p := purgeDryRunProblems("linux", posix, nil, home); len(p) != 0 {
		t.Errorf("posix contract rejected its own output: %v", p)
	}
	if p := purgeDryRunProblems("windows", winOut, unsupported, home); len(p) != 0 {
		t.Errorf("windows contract rejected its own output: %v", p)
	}
	for name, p := range map[string][]string{
		"posix fed the windows refusal":  purgeDryRunProblems("linux", winOut, unsupported, home),
		"windows fed the posix plan":     purgeDryRunProblems("windows", posix, nil, home),
		"windows without its error kind": purgeDryRunProblems("windows", winOut, nil, home),
	} {
		if len(p) == 0 {
			t.Errorf("%s: the checker accepted it", name)
		}
	}
}
