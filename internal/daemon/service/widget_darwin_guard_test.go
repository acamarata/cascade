//go:build darwin

// Purpose: darwin tests for the widget installer's trust guards: Status
//
//	refuses to vouch for a plist cascade did not write or whose program is not
//	the fixed bundle executable, a symlinked or hollow bundle candidate is
//	refused at install, and an unreadable self-report surfaces.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWidgetStatusUnmanagedPlistIsNotTrusted(t *testing.T) {
	f := newWidgetFixture(t)
	path := widgetPlistPath(f.home)
	mustMkdir(t, filepath.Dir(path))
	planted := "<plist><dict><key>Label</key><string>" + WidgetLabel + "</string><key>ProgramArguments</key>" +
		"<array><string>/bin/sh</string><string>-c</string><string>evil</string></array></dict></plist>\n"
	if err := os.WriteFile(path, []byte(planted), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	loadCapturedPrint(t, f)
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Installed || rep.Managed || rep.ProgramValid || rep.BundlePath != "" || rep.BundlePresent || rep.Loaded {
		t.Fatalf("status = %+v, want installed, unmanaged, nothing trusted", rep)
	}
	if len(f.output.calls) != 0 {
		t.Fatalf("launchctl print ran for an unmanaged plist: %v", f.output.calls)
	}
}

func TestWidgetStatusEditedProgramIsNotValid(t *testing.T) {
	cases := map[string]string{
		"other binary": "/bin/sh",
		"outside":      "/Applications/Evil.app/Contents/MacOS/CascadeMenuBar",
	}
	for name, prog := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWidgetFixture(t)
			mustInstall(t, f)
			path := widgetPlistPath(f.home)
			data, _ := readWidgetPlist(t, f)
			edited := strings.Replace(string(data), widgetExecutablePath(f.bundle), prog, 1)
			if edited == string(data) {
				t.Fatal("edit did not change the plist")
			}
			if err := os.WriteFile(path, []byte(edited), 0o644); err != nil { //nolint:gosec // test fixture
				t.Fatal(err)
			}
			rep, err := NewWidgetInstaller().Status(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.Managed || rep.ProgramValid || rep.BundlePath != "" || rep.BundlePresent {
				t.Fatalf("status = %+v, want managed with an invalid program and no bundle", rep)
			}
		})
	}
}

func TestWidgetStatusManagedValidProgram(t *testing.T) {
	f := newWidgetFixture(t)
	mustInstall(t, f)
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil || !rep.Managed || !rep.ProgramValid || rep.BundlePath != f.bundle {
		t.Fatalf("status = %+v, %v; want managed, valid program, bundle %s", rep, err, f.bundle)
	}
}

func TestWidgetStatusSelfReportStates(t *testing.T) {
	f := newWidgetFixture(t)
	mustInstall(t, f)
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil || rep.SelfReport != nil || rep.SelfReportError != "" || !strings.HasSuffix(rep.StatusPath, "widget-status.json") {
		t.Fatalf("no file: %+v, %v; want no report, no error, status path set", rep, err)
	}
	plantSelfReport(t, f, "{not json")
	rep, err = NewWidgetInstaller().Status(f.cfg)
	if err != nil || rep.SelfReport != nil || !strings.Contains(rep.SelfReportError, rep.StatusPath) {
		t.Fatalf("malformed file: %+v, %v; want SelfReportError naming %s", rep, err, rep.StatusPath)
	}
}

func TestInstallRefusesSymlinkedBundle(t *testing.T) {
	f := newWidgetFixture(t)
	target := filepath.Join(f.root, "elsewhere", WidgetBundleName)
	mustBundle(t, target)
	if err := os.RemoveAll(f.bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.bundle); err != nil {
		t.Fatal(err)
	}
	_, err := NewWidgetInstaller().Install(f.cfg)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), f.bundle) {
		t.Fatalf("err = %v, want KindInvalidInput naming %s", err, f.bundle)
	}
	if _, serr := os.Stat(widgetPlistPath(f.home)); !os.IsNotExist(serr) {
		t.Fatalf("a plist was written for a symlinked bundle (stat err %v)", serr)
	}
}

func TestInstallRefusesBundleWithoutExecutable(t *testing.T) {
	for name, mutate := range map[string]func(exe string){
		"missing":        func(exe string) { _ = os.Remove(exe) },
		"not executable": func(exe string) { _ = os.Chmod(exe, 0o644) },
		"directory": func(exe string) {
			_ = os.Remove(exe)
			_ = os.Mkdir(exe, 0o755)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWidgetFixture(t)
			exe := widgetExecutablePath(f.bundle)
			mutate(exe)
			_, err := NewWidgetInstaller().Install(f.cfg)
			if !cascade.HasKind(err, cascade.KindNotFound) || !strings.Contains(err.Error(), exe) {
				t.Fatalf("err = %v, want KindNotFound naming %s", err, exe)
			}
			if len(f.runner.calls) != 0 {
				t.Fatalf("launchctl ran for a hollow bundle: %v", f.runner.calls)
			}
		})
	}
}
