//go:build darwin

// Purpose: darwin tests for WidgetInstaller.Status over the injected HOME,
//
//	the captured `launchctl print` output and a planted self-report file.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// plantSelfReport writes the widget's status file where the plist says.
func plantSelfReport(t *testing.T, f *widgetFixture, body string) {
	t.Helper()
	_, p := readWidgetPlist(t, f)
	path := p.Environment["CASCADE_WIDGET_STATUS_PATH"]
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadCapturedPrint(t *testing.T, f *widgetFixture) {
	t.Helper()
	out, err := os.ReadFile(filepath.Join("testdata", "launchctl_print_widget.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f.output.out = out
}

func TestWidgetStatusNotInstalled(t *testing.T) {
	f := newWidgetFixture(t)
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil || rep.Installed || rep.Loaded || rep.PlistPath != widgetPlistPath(f.home) {
		t.Fatalf("status = %+v, %v; want not installed with the plist path", rep, err)
	}
	if len(f.output.calls) != 0 {
		t.Fatalf("launchctl print ran for an absent plist: %v", f.output.calls)
	}
}

func TestWidgetStatusLoadedAndNotLoaded(t *testing.T) {
	f := newWidgetFixture(t)
	mustInstall(t, f)
	loadCapturedPrint(t, f)
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Installed || !rep.Loaded || rep.State != "not running" || rep.BundlePath != f.bundle || !rep.BundlePresent {
		t.Fatalf("loaded status = %+v", rep)
	}
	if want := "launchctl print gui/501/" + WidgetLabel; len(f.output.calls) != 1 || f.output.calls[0] != want {
		t.Fatalf("print calls = %q, want [%s]", f.output.calls, want)
	}
	f.output.out, f.output.err = []byte("Could not find service\n"), errors.New("exit status 113")
	rep, err = NewWidgetInstaller().Status(f.cfg)
	if err != nil || !rep.Installed || rep.Loaded {
		t.Fatalf("not-loaded status = %+v, %v; want installed, not loaded", rep, err)
	}
}

func TestWidgetStatusSelfReportPlacement(t *testing.T) {
	cases := []struct {
		name, bundle, want string
	}{
		{"healthy", "", PlacementOK},
		{"trash", "/Users/example/.Trash/CascadeMenuBar.app", PlacementRunningFromTrash},
		{"moved", "/Volumes/Other/CascadeMenuBar.app", PlacementMoved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWidgetFixture(t)
			mustInstall(t, f)
			reported := tc.bundle
			if reported == "" {
				reported = f.bundle
			}
			plantSelfReport(t, f, `{"bundle_path":"`+reported+`","window_level":-1,"written_at":"2026-10-12T08:31:00Z","first_rows_at":"2026-10-12T08:31:02Z"}`)
			rep, err := NewWidgetInstaller().Status(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Placement != tc.want || rep.SelfReport == nil || rep.SelfReport.BundlePath != reported {
				t.Fatalf("placement = %q self=%+v, want %q reporting %s", rep.Placement, rep.SelfReport, tc.want, reported)
			}
			if rep.BundlePath != f.bundle {
				t.Fatalf("installed bundle = %q, want %q (both paths must be reported)", rep.BundlePath, f.bundle)
			}
			if rep.SelfReport.WindowLevel == nil || *rep.SelfReport.WindowLevel != -1 || rep.SelfReport.FirstRowsAt == "" {
				t.Fatalf("self-report fields lost: %+v", rep.SelfReport)
			}
		})
	}
}

func TestWidgetStatusMalformedManagedPlistIsEdited(t *testing.T) {
	f := newWidgetFixture(t)
	path := widgetPlistPath(f.home)
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("<plist><dict>"+widgetManagedMarker), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	rep, err := NewWidgetInstaller().Status(f.cfg)
	if err != nil || !rep.Managed || rep.ProgramValid || rep.BundlePath != "" {
		t.Fatalf("status = %+v, %v; want managed, edited (not valid), no bundle", rep, err)
	}
}
