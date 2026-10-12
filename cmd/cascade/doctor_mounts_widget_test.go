// Purpose: tests for the `widget` doctor check's mount: the bindings reach the
//
//	service installer and the daemon client's status.widget call, the real
//	registry mounts the check, the default report leaves it out and
//	`doctor --widget` runs only it.
//
// Constraints: HOME is t.TempDir(); the RPC seam is a fake (no socket).
// SPORT: cmd/cascade/doctor (P1-WID-04).
package main

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeWidgetInstaller records the config it was driven with.
type fakeWidgetInstaller struct {
	cfg       service.WidgetConfig
	status    service.WidgetStatusReport
	err       error
	installed service.WidgetInstallReport
	removed   service.DeltaReport
	verbs     []string
}

func (f *fakeWidgetInstaller) Install(c service.WidgetConfig) (service.WidgetInstallReport, error) {
	f.cfg, f.verbs = c, append(f.verbs, "install")
	return f.installed, f.err
}

func (f *fakeWidgetInstaller) Uninstall(c service.WidgetConfig) (service.DeltaReport, error) {
	f.cfg, f.verbs = c, append(f.verbs, "uninstall")
	return f.removed, f.err
}

func (f *fakeWidgetInstaller) Status(c service.WidgetConfig) (service.WidgetStatusReport, error) {
	f.cfg, f.verbs = c, append(f.verbs, "status")
	return f.status, f.err
}

func TestDoctorWidgetMountBindsDaemonAndClient(t *testing.T) {
	level := -2
	inst := &fakeWidgetInstaller{status: service.WidgetStatusReport{
		Installed: true, Managed: true, ProgramValid: true, Loaded: true,
		BundlePath: "/Applications/CascadeMenuBar.app", BundlePresent: true,
		Placement:  service.PlacementMoved,
		SelfReport: &service.WidgetSelfReport{BundlePath: "/Volumes/X/CascadeMenuBar.app", WindowLevel: &level},
	}}
	deps := fakeWidgetDeps(t, inst)
	calls := 0
	state, probe := widgetDoctorBindingsFrom(deps, func(context.Context) error {
		calls++
		return errors.New("no daemon")
	})

	st, err := state.WidgetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := doctor.WidgetState{
		Installed: true, Loaded: true, BundlePath: "/Applications/CascadeMenuBar.app", BundlePresent: true,
		Placement: doctor.WidgetPlacementMoved, ReportedBundlePath: "/Volumes/X/CascadeMenuBar.app", WindowLevel: &level,
	}
	if st.BundlePath != want.BundlePath || st.Placement != want.Placement || st.ReportedBundlePath != want.ReportedBundlePath ||
		st.WindowLevel == nil || *st.WindowLevel != -2 || !st.Installed || !st.Loaded || !st.BundlePresent ||
		!st.Managed || !st.ProgramValid || !st.SelfReported {
		t.Fatalf("state = %+v, want %+v (the service installer's report)", st, want)
	}
	if len(inst.verbs) != 1 || inst.verbs[0] != "status" {
		t.Fatalf("installer verbs = %v, want a single status read", inst.verbs)
	}

	err = probe.ProbeStatusWidget(context.Background())
	if err == nil || err.Error() != "no daemon" {
		t.Fatalf("probe err = %v, want the client error passed through", err)
	}
	if calls != 1 || daemon.MethodStatusWidget != "status.widget" {
		t.Fatalf("probe issued %d daemon calls (method const %q), want one status.widget call", calls, daemon.MethodStatusWidget)
	}
}

func TestDoctorWidgetStateTreatsUnsupportedPlatformAsNotInstalled(t *testing.T) {
	inst := &fakeWidgetInstaller{err: cascade.New(cascade.KindUnsupported, "widget status: macOS only")}
	state, _ := widgetDoctorBindingsFrom(fakeWidgetDeps(t, inst), nil)
	st, err := state.WidgetState(context.Background())
	if err != nil || st.Installed || !st.Unsupported {
		t.Fatalf("state = %+v, %v; want not installed and unsupported, nil", st, err)
	}
	inst.err = cascade.New(cascade.KindUnavailable, "disk")
	if _, err := state.WidgetState(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want the installer error passed through", err)
	}
}

func TestDoctorRealRegistryMountsWidgetAndFilters(t *testing.T) {
	useTempCustody(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("widget"); !ok {
		t.Fatal("the real productionCheckRegistry does not mount the widget check")
	}
	for _, c := range withoutWidgetCheck(reg.List()) {
		if c.Name() == "widget" {
			t.Fatal("withoutWidgetCheck kept the widget check")
		}
	}
	only := onlyWidgetCheck(reg)
	if len(only) != 1 || only[0].Name() != "widget" {
		t.Fatalf("onlyWidgetCheck = %v, want exactly the widget check", only)
	}
	res, err := only[0].Run(context.Background())
	if goruntime.GOOS != "darwin" {
		// The widget exists only on macOS: elsewhere the check reports OK
		// and has nothing to remediate.
		if err != nil || res.Status != doctor.StatusOK || !strings.Contains(res.Message, "not supported on this platform") {
			t.Fatalf("widget check on %s = %+v, %v; want OK, not supported", goruntime.GOOS, res, err)
		}
		return
	}
	if err != nil || res.Status != doctor.StatusOK || res.Remediation != "cascade widget install" {
		t.Fatalf("widget check on a fresh HOME = %+v, %v; want OK with the install remedy", res, err)
	}
}

func TestDoctorDefaultReportExcludesWidget(t *testing.T) {
	widgetCheck := &fakeCheck{name: doctor.WidgetCheckName, status: doctor.StatusError, message: "WIDGET-RAN"}
	other := &fakeCheck{name: "other-check", status: doctor.StatusOK, message: "OTHER-RAN"}
	out, err := execRootDoctor(t, testDoctorDeps(t, other, widgetCheck), "doctor")
	if err != nil {
		t.Fatalf("default doctor must not run the failing widget check: %v\n%s", err, out)
	}
	if strings.Contains(out, "WIDGET-RAN") || !strings.Contains(out, "OTHER-RAN") {
		t.Fatalf("default report = %q, want the other check only", out)
	}
}

func TestDoctorWidgetFlagRunsOnlyWidgetCheck(t *testing.T) {
	widgetCheck := &fakeCheck{name: doctor.WidgetCheckName, status: doctor.StatusOK, message: "WIDGET-RAN"}
	other := &fakeCheck{name: "other-check", status: doctor.StatusOK, message: "OTHER-RAN"}
	out, err := execRootDoctor(t, testDoctorDeps(t, other, widgetCheck), "doctor", "--widget")
	if err != nil {
		t.Fatalf("doctor --widget: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WIDGET-RAN") || strings.Contains(out, "OTHER-RAN") {
		t.Fatalf("doctor --widget output = %q, want only the widget check", out)
	}
	if _, err := execRootDoctor(t, testDoctorDeps(t, other), "doctor", "--widget"); err == nil {
		t.Fatal("doctor --widget with no widget check registered must refuse, not report a clean empty run")
	}
}
