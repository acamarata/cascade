//go:build linux

// Purpose: linux end-to-end and golden-fixture tests — proves
//
//	NewInstaller()'s real linux Installer against a fake Runner and
//	t.TempDir() home directories, and structurally validates
//	renderSystemdUnit's output against testdata/golden_systemd.service
//	(Art.2 real-counterpart; see testdata/README.md for provenance).
//
// Constraints: Art.7.1 — HomeDir is always t.TempDir(); Runner is always
//
//	fakeRunner (defined in service_test.go); no test here ever invokes a
//	real systemctl.
//
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).
package service

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func testLinuxConfig(t *testing.T, runner Runner) Config {
	t.Helper()
	return Config{
		HomeDir:    t.TempDir(),
		Executable: "/usr/local/bin/cascade",
		LogPath:    filepath.Join(t.TempDir(), "daemon.log"),
		Runner:     runner,
	}
}

// TestLinuxInstall_FreshThenConverges is the platform-real counterpart of
// TestInstallIdempotency: DeltaReport.Action is "installed" then
// "reloaded", and all three systemctl steps ran both times.
func TestLinuxInstall_FreshThenConverges(t *testing.T) {
	runner := &fakeRunner{}
	cfg := testLinuxConfig(t, runner)
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

	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable cascade",
		"systemctl --user start cascade",
	}
	if len(runner.calls) != 6 {
		t.Fatalf("runner.calls = %v, want 6 calls (3 steps x 2 installs)", runner.calls)
	}
	for i, w := range want {
		if runner.calls[i] != w {
			t.Errorf("call[%d] = %q, want %q", i, runner.calls[i], w)
		}
	}
}

// TestLinuxInstall_EnableFailurePropagates is the "exec failure" error
// path: systemctl enable failing must surface as an error, never a
// success DeltaReport (Art.1).
func TestLinuxInstall_EnableFailurePropagates(t *testing.T) {
	runner := &fakeRunner{failOn: map[string]error{
		"systemctl --user enable cascade": os.ErrPermission,
	}}
	cfg := testLinuxConfig(t, runner)

	report, err := NewInstaller().Install(cfg)
	if err == nil {
		t.Fatal("want an error when systemctl enable fails, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err kind = %v, want KindUnavailable", err)
	}
	if report != (DeltaReport{}) {
		t.Errorf("report = %+v, want zero value on failure", report)
	}
	// start must never have been attempted once enable failed.
	for _, c := range runner.calls {
		if strings.Contains(c, "start") {
			t.Errorf("systemctl start was called after enable failed: %v", runner.calls)
		}
	}
}

// TestLinuxInstall_MissingExecutable is the linux-real "missing binary"
// path.
func TestLinuxInstall_MissingExecutable(t *testing.T) {
	cfg := testLinuxConfig(t, &fakeRunner{})
	cfg.Executable = ""
	if _, err := NewInstaller().Install(cfg); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err kind = %v, want KindInvalidInput", err)
	}
}

// TestLinuxInstall_ForeignFileRefused proves the platform Install refuses
// a pre-existing, non-cascade-managed unit rather than clobbering it.
func TestLinuxInstall_ForeignFileRefused(t *testing.T) {
	cfg := testLinuxConfig(t, &fakeRunner{})
	path := systemdUnitPath(cfg.HomeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	foreign := []byte("[Unit]\nDescription=hand written\n")
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatalf("seed foreign unit: %v", err)
	}

	_, err := NewInstaller().Install(cfg)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err kind = %v, want KindConflict", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, foreign) {
		t.Errorf("foreign unit was modified: got %q, want untouched %q", got, foreign)
	}
}

// TestLinuxInstallThenUninstall_RestoresDirectory is the install-then-
// uninstall byte-level check for the systemd path.
func TestLinuxInstallThenUninstall_RestoresDirectory(t *testing.T) {
	runner := &fakeRunner{}
	cfg := testLinuxConfig(t, runner)
	unitDir := filepath.Join(cfg.HomeDir, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	sibling := filepath.Join(unitDir, "other.service")
	siblingContent := []byte("[Unit]\nDescription=other\n")
	if err := os.WriteFile(sibling, siblingContent, 0o644); err != nil {
		t.Fatalf("seed sibling: %v", err)
	}

	inst := NewInstaller()
	if _, err := inst.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := inst.Uninstall(cfg); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	got, err := os.ReadFile(sibling)
	if err != nil {
		t.Fatalf("sibling missing after install+uninstall: %v", err)
	}
	if !bytes.Equal(got, siblingContent) {
		t.Errorf("sibling changed: got %q, want %q", got, siblingContent)
	}
	if _, err := os.Stat(systemdUnitPath(cfg.HomeDir)); !os.IsNotExist(err) {
		t.Errorf("unit still present after uninstall (err=%v)", err)
	}
}

// TestLinuxUninstall_AbsentIsNoop is the platform-real counterpart of
// TestUninstallAbsentUnitIsNoop.
func TestLinuxUninstall_AbsentIsNoop(t *testing.T) {
	cfg := testLinuxConfig(t, &fakeRunner{})
	report, err := NewInstaller().Uninstall(cfg)
	if err != nil {
		t.Fatalf("Uninstall(absent): %v", err)
	}
	if report.Action != ActionNotInstalled {
		t.Errorf("action = %q, want %q", report.Action, ActionNotInstalled)
	}
}

// --- golden fixture validation (Art.2) ---

// parseSystemdUnit structurally parses a unit file into section -> set of
// key names present. Comment lines (leading '#') and blank lines are
// skipped; this is a real INI-shaped structural walk, not a byte/string
// compare against the golden blob.
func parseSystemdUnit(t *testing.T, data []byte) map[string]map[string]string {
	t.Helper()
	sections := map[string]map[string]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if sections[section] == nil {
				sections[section] = map[string]string{}
			}
		default:
			if section == "" {
				t.Fatalf("key/value line %q outside any section", line)
			}
			kv := strings.SplitN(line, "=", 2)
			if len(kv) != 2 {
				t.Fatalf("malformed unit line: %q", line)
			}
			sections[section][kv[0]] = kv[1]
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan unit file: %v", err)
	}
	if len(sections) == 0 {
		t.Fatal("parsed to zero sections — parser or fixture is broken")
	}
	return sections
}

// TestLinuxUnitGolden_RequiredSectionsAndKeysPresent asserts every
// section/key present in the real-counterpart golden ([Unit] Description/
// After, [Service] ExecStart/Type/Restart/RestartSec, [Install] WantedBy)
// is also present in renderSystemdUnit's own output.
func TestLinuxUnitGolden_RequiredSectionsAndKeysPresent(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "golden_systemd.service"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	goldenSections := parseSystemdUnit(t, golden)

	cfg := testLinuxConfig(t, &fakeRunner{})
	genSections := parseSystemdUnit(t, renderSystemdUnit(cfg))

	for section, keys := range goldenSections {
		genKeys, ok := genSections[section]
		if !ok {
			t.Errorf("generated unit missing section [%s]", section)
			continue
		}
		for key := range keys {
			if _, ok := genKeys[key]; !ok {
				t.Errorf("generated unit [%s] missing required key %q", section, key)
			}
		}
	}
}

// TestLinuxUnit_ExecStartReferencesExecutable pins the one structural
// detail a flat key-presence check would miss: ExecStart must actually
// reference Config.Executable.
func TestLinuxUnit_ExecStartReferencesExecutable(t *testing.T) {
	cfg := testLinuxConfig(t, &fakeRunner{})
	sections := parseSystemdUnit(t, renderSystemdUnit(cfg))
	execStart := sections["Service"]["ExecStart"]
	if !strings.Contains(execStart, cfg.Executable) {
		t.Errorf("ExecStart = %q, want it to contain %q", execStart, cfg.Executable)
	}
}

// TestLinuxUnit_Escaping proves an executable path containing whitespace
// or a quote is quoted per systemd's word-splitting rules rather than
// corrupting ExecStart into multiple argv entries.
func TestLinuxUnit_Escaping(t *testing.T) {
	tricky := []string{
		"/opt/cascade bin/cascade",
		`/opt/"cascade"/bin/cascade`,
		"/opt/cascade's-dir/bin/cascade",
	}
	for _, exe := range tricky {
		t.Run(exe, func(t *testing.T) {
			cfg := testLinuxConfig(t, &fakeRunner{})
			cfg.Executable = exe
			sections := parseSystemdUnit(t, renderSystemdUnit(cfg))
			execStart := sections["Service"]["ExecStart"]
			quoted := systemdQuoteArg(exe)
			if !strings.HasPrefix(execStart, quoted) {
				t.Errorf("ExecStart = %q, want it to start with quoted %q", execStart, quoted)
			}
		})
	}
}

// TestLinuxUnit_ManagedMarkerPresent proves the clobber-refusal marker
// this package's writeManagedFile relies on is actually emitted.
func TestLinuxUnit_ManagedMarkerPresent(t *testing.T) {
	cfg := testLinuxConfig(t, &fakeRunner{})
	data := renderSystemdUnit(cfg)
	if !isManagedUnit(data) {
		t.Error("renderSystemdUnit output does not carry the managed marker")
	}
}
