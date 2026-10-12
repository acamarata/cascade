//go:build darwin

// Purpose: darwin tests for the widget LaunchAgent installer: absolute bundle
//
//	path, recorded environment, no environment override, bundle refusals,
//	idempotent install, launchctl ordering, uninstall and the unmanaged-plist
//	refusal. Every test runs against widgetFixture (injected HOME, recording
//	Runner); none touches launchd or the real ~/Library.
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func mustInstall(t *testing.T, f *widgetFixture) WidgetInstallReport {
	t.Helper()
	rep, err := NewWidgetInstaller().Install(f.cfg)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return rep
}

func readWidgetPlist(t *testing.T, f *widgetFixture) ([]byte, widgetPlist) {
	t.Helper()
	data, err := os.ReadFile(widgetPlistPath(f.home))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	p, err := parseWidgetPlist(data)
	if err != nil {
		t.Fatalf("parse plist: %v", err)
	}
	return data, p
}

func TestLaunchAgentUsesAbsoluteBundlePath(t *testing.T) {
	f := newWidgetFixture(t)
	mustInstall(t, f)
	data, p := readWidgetPlist(t, f)
	want := []string{filepath.Join(f.bundle, "Contents", "MacOS", "CascadeMenuBar")}
	if !reflect.DeepEqual(p.ProgramArguments, want) {
		t.Fatalf("ProgramArguments = %q, want exactly %q", p.ProgramArguments, want)
	}
	if !filepath.IsAbs(p.ProgramArguments[0]) || strings.Contains(string(data), "<string>open</string>") {
		t.Fatalf("ProgramArguments must be one absolute executable, got %q", p.ProgramArguments)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "golden_launchd_widget.plist"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(string(data), f.home, "/Users/example")
	got = strings.ReplaceAll(got, "\t<key>X-Cascade-Managed</key>\n\t<true/>\n", "")
	if got != string(golden) {
		t.Fatalf("normalised plist differs from golden\n--- got\n%s\n--- want\n%s", got, golden)
	}
}

func TestLaunchAgentRecordsCLIPath(t *testing.T) {
	cases := []struct {
		name, cascadeHome string
	}{{"unset", ""}, {"set", "/custom/cascade-home"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWidgetFixture(t)
			f.cfg.Getenv = func(k string) string {
				if k == "CASCADE_HOME" {
					return tc.cascadeHome
				}
				return ""
			}
			mustInstall(t, f)
			_, p := readWidgetPlist(t, f)
			want := map[string]string{
				"CASCADE_CLI_PATH":           f.realCLI,
				"CASCADE_SOCKET_PATH":        f.cfg.Paths.SocketPath(),
				"CASCADE_WIDGET_STATUS_PATH": filepath.Join(f.cfg.Paths.DataDir(), "widget", "widget-status.json"),
			}
			if tc.cascadeHome != "" {
				want["CASCADE_HOME"] = tc.cascadeHome
			}
			if !reflect.DeepEqual(p.Environment, want) {
				t.Fatalf("EnvironmentVariables = %v, want exactly %v", p.Environment, want)
			}
			if p.Environment["CASCADE_CLI_PATH"] == f.cfg.Executable {
				t.Fatalf("CASCADE_CLI_PATH kept the symlink %s instead of its target", f.cfg.Executable)
			}
		})
	}
}

func TestInstallIgnoresEnvironmentOverride(t *testing.T) {
	f := newWidgetFixture(t)
	alt := filepath.Join(f.root, "alt", WidgetBundleName)
	mustBundle(t, alt)
	for _, k := range []string{"CASCADE_WIDGET_BUNDLE", "CASCADE_WIDGET_APP", "CASCADE_MENUBAR_APP", "CASCADE_WIDGET_BUNDLE_PATH"} {
		t.Setenv(k, alt)
	}
	f.cfg.Getenv = func(k string) string {
		if k == "CASCADE_HOME" {
			return ""
		}
		return alt
	}
	mustInstall(t, f)
	_, p := readWidgetPlist(t, f)
	want := filepath.Join(f.bundle, "Contents", "MacOS", "CascadeMenuBar")
	if len(p.ProgramArguments) != 1 || p.ProgramArguments[0] != want {
		t.Fatalf("ProgramArguments = %q, want [%s]; an environment variable steered the bundle", p.ProgramArguments, want)
	}
	if strings.Contains(strings.Join(p.ProgramArguments, ""), "alt") {
		t.Fatalf("override bundle leaked into ProgramArguments: %q", p.ProgramArguments)
	}
}

func TestInstallSystemBundleWinsOverHome(t *testing.T) {
	f := newWidgetFixture(t)
	sys := filepath.Join(f.sysRoot, "Applications", WidgetBundleName)
	mustBundle(t, sys)
	mustInstall(t, f)
	_, p := readWidgetPlist(t, f)
	if want := filepath.Join(sys, "Contents", "MacOS", "CascadeMenuBar"); p.ProgramArguments[0] != want {
		t.Fatalf("ProgramArguments[0] = %s, want the /Applications candidate %s", p.ProgramArguments[0], want)
	}
}

func TestInstallRefusesNonMenuBarBundle(t *testing.T) {
	f := newWidgetFixture(t)
	other := filepath.Join(f.root, "Other.app")
	mustMkdir(t, other)
	f.cfg.Candidates = []string{other}
	_, err := NewWidgetInstaller().Install(f.cfg)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), other) {
		t.Fatalf("err = %v, want KindInvalidInput naming %s", err, other)
	}
	if _, serr := os.Stat(widgetPlistPath(f.home)); !os.IsNotExist(serr) {
		t.Fatalf("a plist was written for a refused bundle (stat err %v)", serr)
	}
	if len(f.runner.calls) != 0 {
		t.Fatalf("launchctl ran for a refused bundle: %v", f.runner.calls)
	}
}

func TestInstallNoBundleNamesBothCandidates(t *testing.T) {
	f := newWidgetFixture(t)
	if err := os.RemoveAll(f.bundle); err != nil {
		t.Fatal(err)
	}
	_, err := NewWidgetInstaller().Install(f.cfg)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
	for _, p := range []string{filepath.Join(f.sysRoot, "Applications", WidgetBundleName), f.bundle} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not name candidate %s", err, p)
		}
	}
}
