// Purpose: tests that the widget doctor check and `widget status` refuse to
//
//	vouch for a launch agent cascade did not write, or one whose program was
//	edited, and that a loaded widget without a self-report is a WARN.
//
// SPORT: cmd/cascade/doctor (P1-WID-04).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/doctor"
)

// runWidgetDoctor runs the real widget check over the real state binding and a
// daemon that answers, for the installer report rep.
func runWidgetDoctor(t *testing.T, rep service.WidgetStatusReport) doctor.CheckResult {
	t.Helper()
	deps := fakeWidgetDeps(t, &fakeWidgetInstaller{status: rep})
	state, probe := widgetDoctorBindingsFrom(deps, func(context.Context) error { return nil })
	res, err := doctor.NewWidgetCheck(state, probe).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDoctorWidgetUnmanagedPlistIsError(t *testing.T) {
	res := runWidgetDoctor(t, service.WidgetStatusReport{Installed: true, Loaded: true})
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "a launch agent not written by cascade holds com.acamarata.cascade.widget") {
		t.Fatalf("unmanaged plist = %+v, want the not-written-by-cascade ERROR", res)
	}
	if !strings.Contains(res.Remediation, "remove") {
		t.Fatalf("remediation = %q, want inspect and remove", res.Remediation)
	}
}

func TestDoctorWidgetEditedProgramIsError(t *testing.T) {
	res := runWidgetDoctor(t, service.WidgetStatusReport{Installed: true, Managed: true, Loaded: true})
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "does not run the CascadeMenuBar app") {
		t.Fatalf("edited program = %+v, want ERROR", res)
	}
}

func TestDoctorWidgetLoadedWithoutSelfReportWarns(t *testing.T) {
	rep := service.WidgetStatusReport{
		Installed: true, Managed: true, ProgramValid: true, Loaded: true, State: "not running",
		BundlePresent: true, StatusPath: "/Users/example/widget-status.json",
	}
	res := runWidgetDoctor(t, rep)
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "has not reported (state not running)") ||
		!strings.Contains(res.Message, rep.StatusPath) {
		t.Fatalf("no self-report = %+v, want WARN naming the state and status path", res)
	}
	rep.SelfReport = &service.WidgetSelfReport{BundlePath: "/Applications/CascadeMenuBar.app"}
	if res := runWidgetDoctor(t, rep); res.Status != doctor.StatusOK {
		t.Fatalf("healthy with a self-report = %+v, want OK", res)
	}
}

func TestWidgetStatusShowsUnmanagedAndEditedProgram(t *testing.T) {
	inst := &fakeWidgetInstaller{status: service.WidgetStatusReport{Installed: true, PlistPath: "/p.plist"}}
	deps := fakeWidgetDeps(t, inst)
	out, err := execWidget(t, deps, "status")
	if err != nil || !strings.Contains(out, "managed: no") {
		t.Fatalf("unmanaged status = %q, %v", out, err)
	}
	inst.status.Managed = true
	if out, _ = execWidget(t, deps, "status"); !strings.Contains(out, "program valid: no") {
		t.Fatalf("edited-program status = %q", out)
	}
	jout, err := execWidget(t, deps, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(jout), &env); err != nil {
		t.Fatal(err)
	}
	if m, ok := env.Data["managed"].(bool); !ok || !m {
		t.Fatalf("json managed = %v in %s", env.Data["managed"], jout)
	}
	if pv, ok := env.Data["program_valid"].(bool); !ok || pv {
		t.Fatalf("json program_valid = %v in %s", env.Data["program_valid"], jout)
	}
}
