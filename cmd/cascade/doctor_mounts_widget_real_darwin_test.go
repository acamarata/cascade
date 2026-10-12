//go:build darwin

// Purpose: the widget doctor check over the REAL darwin installer and the real
//
//	state binding: a plist the installer wrote is accepted, and the same plist
//	with an added Program key is a doctor ERROR.
//
// Constraints: HOME is t.TempDir(); the Runner is a recording stub (no launchctl).
// SPORT: cmd/cascade/doctor (P1-WID-04).
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/doctor"
)

type noopRunner struct{}

func (noopRunner) Run(string, ...string) error { return nil }

// realInstalledWidget installs through the real installer into a temp HOME and
// returns the deps and the plist path.
func realInstalledWidget(t *testing.T) (widgetDeps, string) {
	t.Helper()
	deps := fakeWidgetDeps(t, service.NewWidgetInstaller())
	home, _ := deps.HomeDir()
	exe := filepath.Join(home, "Applications", service.WidgetBundleName, "Contents", "MacOS", "CascadeMenuBar")
	cli := filepath.Join(home, "bin", "cascade")
	for _, f := range []string{exe, cli} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable stand-in
			t.Fatal(err)
		}
	}
	deps.Executable = func() (string, error) { return cli, nil }
	deps.Runner = noopRunner{}
	if _, err := execWidget(t, deps, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}
	return deps, filepath.Join(home, "Library", "LaunchAgents", service.WidgetLabel+".plist")
}

func runRealWidgetDoctor(t *testing.T, deps widgetDeps) doctor.CheckResult {
	t.Helper()
	state, probe := widgetDoctorBindingsFrom(deps, func(context.Context) error { return nil })
	res, err := doctor.NewWidgetCheck(state, probe).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDoctorWidgetRealPlistWithAddedProgramIsError(t *testing.T) {
	deps, plist := realInstalledWidget(t)
	if res := runRealWidgetDoctor(t, deps); res.Status == doctor.StatusError {
		t.Fatalf("installer-written plist = %+v, want not ERROR", res)
	}
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "<dict>\n", "<dict>\n\t<key>Program</key>\n\t<string>/bin/sh</string>\n", 1)
	if edited == string(data) {
		t.Fatal("edit did not change the plist")
	}
	if err := os.WriteFile(plist, []byte(edited), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	res := runRealWidgetDoctor(t, deps)
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "differs from what cascade installs") {
		t.Fatalf("added Program key = %+v, want the differs-from-installer ERROR", res)
	}
	if !strings.Contains(res.Remediation, "cascade widget uninstall") {
		t.Fatalf("remediation = %q, want uninstall then install", res.Remediation)
	}
}

func TestDoctorWidgetRealPlistWithRewrittenCLIPathIsError(t *testing.T) {
	deps, plist := realInstalledWidget(t)
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	cli, _ := deps.Executable()
	resolved, err := filepath.EvalSymlinks(cli)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "<string>"+resolved+"</string>", "<string>"+filepath.Join(filepath.Dir(resolved), "other")+"</string>", 1)
	if edited == string(data) {
		t.Fatal("edit did not change the plist")
	}
	if err := os.WriteFile(plist, []byte(edited), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if res := runRealWidgetDoctor(t, deps); res.Status != doctor.StatusError {
		t.Fatalf("rewritten CLI path = %+v, want ERROR", res)
	}
}

// A <key> in value position is read by launchd as a string, so the edit makes
// launchd run /tmp/evil while a field-by-field reader sees nothing. Status
// compares bytes, so doctor must report ERROR.
func TestDoctorWidgetRealPlistWithKeyAsValueProgramIsError(t *testing.T) {
	deps, plist := realInstalledWidget(t)
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "<dict>\n", "<dict>\n\t<key>Program</key><key>/tmp/evil</key>\n", 1)
	if edited == string(data) {
		t.Fatal("edit did not change the plist")
	}
	if err := os.WriteFile(plist, []byte(edited), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if res := runRealWidgetDoctor(t, deps); res.Status != doctor.StatusError {
		t.Fatalf("key-as-value Program = %+v, want ERROR", res)
	}
}
