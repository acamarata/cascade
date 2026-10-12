// Purpose: tests for `cascade widget install|uninstall|status`: help lists the
//
//	verbs, each verb drives the installer with the injected deps, --json
//	carries the DeltaReport and StatusReport shapes (installed_at included),
//	and installer errors surface unchanged.
//
// Constraints: a fake installer and fake deps; nothing touches launchd.
// SPORT: cmd/cascade/widget (P1-WID-04).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func fakeWidgetDeps(t *testing.T, inst service.WidgetInstaller) widgetDeps {
	t.Helper()
	home := t.TempDir()
	return widgetDeps{
		Installer:  inst,
		HomeDir:    func() (string, error) { return home, nil },
		Getuid:     func() int { return 501 },
		Executable: func() (string, error) { return "/opt/cascade/bin/cascade", nil },
		Paths:      doctorTestPaths(t),
		Getenv:     func(k string) string { return map[string]string{"CASCADE_HOME": "/h"}[k] },
		Clock:      runtime.NewFixedClock(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)),
		Runner:     nil,
		Output:     nil,
	}
}

// execWidget runs args against the real root with the widget command swapped
// for one built from deps.
func execWidget(t *testing.T, deps widgetDeps, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	existing, _, err := root.Find([]string{"widget"})
	if err != nil || existing.Name() != "widget" {
		t.Fatalf("widget is not mounted on the root command: %v", err)
	}
	root.RemoveCommand(existing)
	cmd := newWidgetCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(append([]string{"widget"}, args...))
	err = root.Execute()
	return buf.String(), err
}

func TestWidgetHelpListsVerbs(t *testing.T) {
	out, err := execWidget(t, fakeWidgetDeps(t, &fakeWidgetInstaller{}), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"install", "uninstall", "status"} {
		if !strings.Contains(out, verb) {
			t.Errorf("widget --help does not list %q:\n%s", verb, out)
		}
	}
}

func TestWidgetInstallCommandDrivesInstaller(t *testing.T) {
	inst := &fakeWidgetInstaller{installed: service.WidgetInstallReport{
		DeltaReport: service.DeltaReport{Action: service.ActionReloaded, Detail: "reloaded at /p"},
		InstalledAt: "2026-10-12T08:30:00Z",
	}}
	deps := fakeWidgetDeps(t, inst)
	out, err := execWidget(t, deps, "install")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "reloaded") || !strings.Contains(out, "2026-10-12T08:30:00Z") {
		t.Fatalf("human output = %q", out)
	}
	c := inst.cfg
	home, _ := deps.HomeDir()
	if c.HomeDir != home || c.UID != 501 || c.Executable != "/opt/cascade/bin/cascade" || c.Getenv("CASCADE_HOME") != "/h" ||
		c.Clock == nil || c.Paths == nil {
		t.Fatalf("installer config = %+v, want the injected deps", c)
	}
	jout, err := execWidget(t, deps, "install", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"action":"reloaded"`, `"installed_at":"2026-10-12T08:30:00Z"`} {
		if !strings.Contains(strings.ReplaceAll(jout, " ", ""), want) {
			t.Errorf("--json output lacks %s:\n%s", want, jout)
		}
	}
}

func TestWidgetUninstallAndStatusCommands(t *testing.T) {
	inst := &fakeWidgetInstaller{removed: service.DeltaReport{Action: service.ActionNotInstalled, Detail: "no agent"}}
	deps := fakeWidgetDeps(t, inst)
	if out, err := execWidget(t, deps, "uninstall"); err != nil || !strings.Contains(out, "not installed") {
		t.Fatalf("uninstall = %q, %v", out, err)
	}
	level := 0
	inst.status = service.WidgetStatusReport{
		Installed: true, Managed: true, ProgramValid: true, Loaded: true, State: "running", PlistPath: "/p.plist", BundlePath: "/Applications/CascadeMenuBar.app",
		BundlePresent: true, Placement: service.PlacementRunningFromTrash,
		SelfReport: &service.WidgetSelfReport{BundlePath: "/Users/e/.Trash/CascadeMenuBar.app", WindowLevel: &level},
	}
	out, err := execWidget(t, deps, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"loaded: yes (running)", "/Applications/CascadeMenuBar.app", "/Users/e/.Trash/CascadeMenuBar.app", "RUNNING_FROM_TRASH", "window level: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	jout, err := execWidget(t, deps, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data service.WidgetStatusReport `json:"data"`
	}
	if err := json.Unmarshal([]byte(jout), &env); err != nil {
		t.Fatalf("status --json is not an envelope: %v\n%s", err, jout)
	}
	if env.Data.Placement != "RUNNING_FROM_TRASH" || !env.Data.Loaded || env.Data.SelfReport == nil {
		t.Fatalf("status --json data = %+v", env.Data)
	}
	inst.status = service.WidgetStatusReport{}
	if out, _ := execWidget(t, deps, "status"); !strings.Contains(out, "not installed") {
		t.Fatalf("not-installed status = %q", out)
	}
}

func TestWidgetCommandErrors(t *testing.T) {
	inst := &fakeWidgetInstaller{err: cascade.New(cascade.KindConflict, "refusing to overwrite")}
	if _, err := execWidget(t, fakeWidgetDeps(t, inst), "install"); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("install err = %v, want the installer's KindConflict", err)
	}
	deps := fakeWidgetDeps(t, &fakeWidgetInstaller{})
	deps.HomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := execWidget(t, deps, "status"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("status err = %v, want KindUnavailable", err)
	}
	deps = fakeWidgetDeps(t, &fakeWidgetInstaller{})
	deps.Executable = func() (string, error) { return "", errors.New("no exe") }
	if _, err := execWidget(t, deps, "install"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("install err = %v, want KindUnavailable", err)
	}
	if _, err := execWidget(t, fakeWidgetDeps(t, &fakeWidgetInstaller{}), "bogus"); err == nil {
		t.Fatal("an unknown widget subcommand must fail")
	}
}
