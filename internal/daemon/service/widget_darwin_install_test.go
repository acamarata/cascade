//go:build darwin

// Purpose: darwin tests for the widget installer's launchctl choreography:
//
//	idempotent reinstall, bootout-before-bootstrap ordering, nothing written
//	outside the injected HOME, installed_at from the injected clock, uninstall
//	and the unmanaged-plist refusal.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWidgetInstallIdempotent(t *testing.T) {
	f := newWidgetFixture(t)
	first := mustInstall(t, f)
	if first.Action != ActionInstalled {
		t.Fatalf("first action = %q, want %q", first.Action, ActionInstalled)
	}
	before, _ := readWidgetPlist(t, f)
	second := mustInstall(t, f)
	if second.Action != ActionReloaded {
		t.Fatalf("second action = %q, want %q", second.Action, ActionReloaded)
	}
	after, _ := readWidgetPlist(t, f)
	if !bytes.Equal(before, after) {
		t.Fatal("a repeat install changed the plist bytes")
	}
}

func TestWidgetInstallUsesInjectedLaunchctl(t *testing.T) {
	f := newWidgetFixture(t)
	snapshot := func() map[string]bool {
		out := map[string]bool{}
		_ = filepath.WalkDir(f.root, func(p string, _ fs.DirEntry, err error) error {
			if err == nil && !strings.HasPrefix(p, f.home) {
				out[p] = true
			}
			return nil
		})
		return out
	}
	before := snapshot()
	mustInstall(t, f)
	plist := widgetPlistPath(f.home)
	want := []string{
		"launchctl bootout gui/501/com.acamarata.cascade.widget",
		"launchctl bootstrap gui/501 " + plist,
	}
	if !reflect.DeepEqual(f.runner.calls, want) {
		t.Fatalf("launchctl calls = %q, want %q (bootout first)", f.runner.calls, want)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("install wrote outside the injected HOME: before %v after %v", before, after)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist not under the injected HOME: %v", err)
	}
}

func TestWidgetInstallReportsInstalledAtFromClock(t *testing.T) {
	f := newWidgetFixture(t)
	if got := mustInstall(t, f).InstalledAt; got != "2026-10-12T08:30:00Z" {
		t.Fatalf("installed_at = %q, want the injected clock 2026-10-12T08:30:00Z", got)
	}
}

func TestWidgetInstallBootstrapFailureSurfaces(t *testing.T) {
	f := newWidgetFixture(t)
	key := "launchctl bootstrap gui/501 " + widgetPlistPath(f.home)
	f.runner.failOn = map[string]error{key: errors.New("boom")}
	_, err := NewWidgetInstaller().Install(f.cfg)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("err = %v, want KindUnavailable naming bootstrap", err)
	}
}

func TestWidgetInstallValidatesInputs(t *testing.T) {
	f := newWidgetFixture(t)
	cases := map[string]func(*WidgetConfig){
		"home":       func(c *WidgetConfig) { c.HomeDir = "" },
		"executable": func(c *WidgetConfig) { c.Executable = "" },
		"paths":      func(c *WidgetConfig) { c.Paths = nil },
		"clock":      func(c *WidgetConfig) { c.Clock = nil },
		"runner":     func(c *WidgetConfig) { c.Runner = nil },
	}
	for name, mut := range cases {
		cfg := f.cfg
		mut(&cfg)
		if _, err := NewWidgetInstaller().Install(cfg); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
	cfg := f.cfg
	cfg.Executable = filepath.Join(f.root, "missing")
	if _, err := NewWidgetInstaller().Install(cfg); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("missing executable: err = %v, want KindNotFound", err)
	}
}

func TestWidgetUninstallRemovesThenReportsNotInstalled(t *testing.T) {
	f := newWidgetFixture(t)
	inst := NewWidgetInstaller()
	rep, err := inst.Uninstall(f.cfg)
	if err != nil || rep.Action != ActionNotInstalled {
		t.Fatalf("uninstall on absent = %+v, %v; want not installed, nil", rep, err)
	}
	if len(f.runner.calls) != 0 {
		t.Fatalf("launchctl ran for an absent unit: %v", f.runner.calls)
	}
	mustInstall(t, f)
	f.runner.calls = nil
	rep, err = inst.Uninstall(f.cfg)
	if err != nil || rep.Action != ActionRemoved {
		t.Fatalf("uninstall = %+v, %v; want removed, nil", rep, err)
	}
	if len(f.runner.calls) != 1 || f.runner.calls[0] != "launchctl bootout gui/501/com.acamarata.cascade.widget" {
		t.Fatalf("launchctl calls = %q, want one bootout", f.runner.calls)
	}
	if _, err := os.Stat(widgetPlistPath(f.home)); !os.IsNotExist(err) {
		t.Fatalf("plist still present after uninstall (stat err %v)", err)
	}
	if rep, err = inst.Uninstall(f.cfg); err != nil || rep.Action != ActionNotInstalled {
		t.Fatalf("second uninstall = %+v, %v; want not installed, nil", rep, err)
	}
}

func TestWidgetUnmanagedPlistRefused(t *testing.T) {
	f := newWidgetFixture(t)
	path := widgetPlistPath(f.home)
	mustMkdir(t, filepath.Dir(path))
	foreign := []byte("<plist><dict><key>Label</key><string>mine</string></dict></plist>\n")
	if err := os.WriteFile(path, foreign, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	inst := NewWidgetInstaller()
	if _, err := inst.Install(f.cfg); !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Install err = %v, want KindConflict naming %s", err, path)
	}
	if _, err := inst.Uninstall(f.cfg); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Uninstall err = %v, want KindConflict", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, foreign) {
		t.Fatalf("unmanaged plist changed: %q", got)
	}
	if len(f.runner.calls) != 0 {
		t.Fatalf("launchctl ran against an unmanaged unit: %v", f.runner.calls)
	}
	if _, serr := os.Stat(f.cfg.Paths.LogDir()); !os.IsNotExist(serr) {
		t.Fatalf("a refused install created the log dir (stat err %v)", serr)
	}
}
