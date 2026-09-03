//go:build darwin

// Purpose: darwin end-to-end and golden-fixture tests — proves
//
//	NewInstaller()'s real darwin Installer against a fake Runner and
//	t.TempDir() home directories, and structurally validates
//	renderLaunchdPlist's output against testdata/golden_launchd.plist
//	(Art.2 real-counterpart; see testdata/README.md for provenance).
//
// Constraints: Art.7.1 — HomeDir is always t.TempDir(); Runner is always
//
//	fakeRunner (defined in service_test.go); no test here ever invokes a
//	real launchctl.
//
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).
package service

import (
	"bytes"
	"github.com/acamarata/cascade/pkg/cascade"
	"os"
	"path/filepath"
	"testing"
)

func testConfig(t *testing.T, runner Runner) Config {
	t.Helper()
	return Config{
		HomeDir:    t.TempDir(),
		Executable: "/usr/local/bin/cascade",
		LogPath:    filepath.Join(t.TempDir(), "daemon.log"),
		UID:        501,
		Runner:     runner,
	}
}

// TestDarwinInstall_FreshThenConverges is the platform-real counterpart of
// TestInstallIdempotency: the SAME two-generation-install shape, but
// through the actual darwinInstaller.Install, proving DeltaReport.Action
// is "installed" then "reloaded" and that both launchctl steps ran.
func TestDarwinInstall_FreshThenConverges(t *testing.T) {
	runner := &fakeRunner{}
	cfg := testConfig(t, runner)
	inst := NewInstaller()

	report, err := inst.Install(cfg)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if report.Action != ActionInstalled {
		t.Errorf("first Install action = %q, want %q", report.Action, ActionInstalled)
	}

	report, err = inst.Install(cfg)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if report.Action != ActionReloaded {
		t.Errorf("second Install action = %q, want %q", report.Action, ActionReloaded)
	}

	wantCalls := []string{
		"launchctl bootout gui/501/com.acamarata.cascade",
		"launchctl bootstrap gui/501 " + launchAgentPath(cfg.HomeDir),
	}
	if got := len(runner.calls); got != 4 {
		t.Fatalf("runner.calls = %v, want 4 calls (bootout+bootstrap twice)", runner.calls)
	}
	if runner.calls[0] != wantCalls[0] || runner.calls[1] != wantCalls[1] {
		t.Errorf("first install calls = %v, want %v", runner.calls[:2], wantCalls)
	}
}

// TestDarwinInstall_BootstrapFailurePropagates is the "exec failure" error
// path: launchctl bootstrap failing must surface as an error, never a
// success DeltaReport (Art.1).
func TestDarwinInstall_BootstrapFailurePropagates(t *testing.T) {
	cfg := testConfig(t, nil)
	path := launchAgentPath(cfg.HomeDir)
	runner := &fakeRunner{failOn: map[string]error{
		"launchctl bootstrap gui/501 " + path: os.ErrPermission,
	}}
	cfg.Runner = runner

	report, err := NewInstaller().Install(cfg)
	if err == nil {
		t.Fatal("want an error when launchctl bootstrap fails, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err kind = %v, want KindUnavailable", err)
	}
	if report != (DeltaReport{}) {
		t.Errorf("report = %+v, want zero value on failure", report)
	}
}

// TestDarwinInstall_MissingExecutable is the darwin-real "missing binary"
// path.
func TestDarwinInstall_MissingExecutable(t *testing.T) {
	cfg := testConfig(t, &fakeRunner{})
	cfg.Executable = ""
	if _, err := NewInstaller().Install(cfg); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err kind = %v, want KindInvalidInput", err)
	}
}

// TestDarwinInstall_ForeignFileRefused proves the platform Install refuses
// a pre-existing, non-cascade-managed plist rather than clobbering it.
func TestDarwinInstall_ForeignFileRefused(t *testing.T) {
	cfg := testConfig(t, &fakeRunner{})
	path := launchAgentPath(cfg.HomeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	foreign := []byte("<plist><dict><key>Label</key><string>not-cascade</string></dict></plist>")
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatalf("seed foreign plist: %v", err)
	}

	_, err := NewInstaller().Install(cfg)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err kind = %v, want KindConflict", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, foreign) {
		t.Errorf("foreign plist was modified: got %q, want untouched %q", got, foreign)
	}
}

// TestDarwinInstallThenUninstall_RestoresDirectory is the install-then-
// uninstall byte-level check: any sibling file already present in
// ~/Library/LaunchAgents is untouched, and the plist path itself returns
// to "does not exist" after uninstall — exactly the state it was in
// before Install ever ran.
func TestDarwinInstallThenUninstall_RestoresDirectory(t *testing.T) {
	runner := &fakeRunner{}
	cfg := testConfig(t, runner)
	agentsDir := filepath.Join(cfg.HomeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	sibling := filepath.Join(agentsDir, "com.example.other.plist")
	siblingContent := []byte("<plist><dict><key>Label</key><string>com.example.other</string></dict></plist>")
	if err := os.WriteFile(sibling, siblingContent, 0o644); err != nil {
		t.Fatalf("seed sibling: %v", err)
	}
	before := snapshotDir(t, agentsDir)

	inst := NewInstaller()
	if _, err := inst.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := inst.Uninstall(cfg); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	after := snapshotDir(t, agentsDir)
	if len(before) != len(after) {
		t.Fatalf("directory entry count changed: before=%v after=%v", before, after)
	}
	for name, wantContent := range before {
		gotContent, ok := after[name]
		if !ok {
			t.Fatalf("sibling entry %q missing after install+uninstall", name)
		}
		if !bytes.Equal(gotContent, wantContent) {
			t.Fatalf("sibling entry %q changed: before=%q after=%q", name, wantContent, gotContent)
		}
	}
	if _, err := os.Stat(launchAgentPath(cfg.HomeDir)); !os.IsNotExist(err) {
		t.Errorf("plist still present after uninstall (err=%v)", err)
	}
}

// TestDarwinUninstall_AbsentIsNoop is the platform-real counterpart of
// TestUninstallAbsentUnitIsNoop.
func TestDarwinUninstall_AbsentIsNoop(t *testing.T) {
	cfg := testConfig(t, &fakeRunner{})
	report, err := NewInstaller().Uninstall(cfg)
	if err != nil {
		t.Fatalf("Uninstall(absent): %v", err)
	}
	if report.Action != ActionNotInstalled {
		t.Errorf("action = %q, want %q", report.Action, ActionNotInstalled)
	}
}

// TestDarwinUninstall_MissingHomeDir is Uninstall's own "missing home
// directory" validation path (requireHomeDir), independent of Install's.
func TestDarwinUninstall_MissingHomeDir(t *testing.T) {
	_, err := NewInstaller().Uninstall(Config{Runner: &fakeRunner{}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err kind = %v, want KindInvalidInput", err)
	}
}

// TestDarwinUninstall_RemoveFailure proves a failing removal (os.Remove
// denied) propagates as KindPermissionDenied rather than a silent
// ActionRemoved (Art.1) — the directory is made read-only AFTER the plist
// is installed, so os.Stat still succeeds but os.Remove cannot unlink the
// entry from its (now write-protected) parent directory.
func TestDarwinUninstall_RemoveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed")
	}
	cfg := testConfig(t, &fakeRunner{})
	inst := NewInstaller()
	if _, err := inst.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	agentsDir := filepath.Dir(launchAgentPath(cfg.HomeDir))
	if err := os.Chmod(agentsDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentsDir, 0o755) })

	_, err := inst.Uninstall(cfg)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("err kind = %v, want KindPermissionDenied", err)
	}
}

// snapshotDir reads every regular file directly inside dir and returns a
// name->content map, used to prove byte-level restoration.
func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	out := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), err)
		}
		out[e.Name()] = content
	}
	return out
}

// --- golden fixture validation (Art.2) ---
