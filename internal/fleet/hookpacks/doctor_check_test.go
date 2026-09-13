// Purpose: exercises CompletionGateDoctorCheck.Run's three outcomes:
// StatusError when the pack is missing, StatusWarn when the probe reports
// the daemon unreachable while a job is active (TestDoctorHarnessDaemonUnreachable),
// and StatusOK otherwise.
// SPORT: fleet/hookpacks.CompletionGateDoctorCheck/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestDoctorCompletionGate_FailsWhenPackNotRegistered(t *testing.T) {
	check := hookpacks.NewCompletionGateDoctorCheck(hookpacks.NewHookRegistry(), nil)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run with no pack registered = %+v, want StatusError", res)
	}
}

func TestDoctorCompletionGate_OKWhenRegisteredAndNoProbe(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, fakeGate{ok: true}, fakeResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	check := hookpacks.NewCompletionGateDoctorCheck(reg, nil)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with pack registered, no probe = %+v, want StatusOK", res)
	}
}

// TestDoctorHarnessDaemonUnreachable is the exact name this ticket's
// checks list runs: the pack is registered (so the FAIL leg does not
// fire), but the injected LivenessProbe reports the daemon unreachable
// while a job is active, which must WARN, per R-21.176.
func TestDoctorHarnessDaemonUnreachable(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, fakeGate{ok: true}, fakeResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	probe := func(context.Context) (bool, bool, error) { return false, true, nil }
	check := hookpacks.NewCompletionGateDoctorCheck(reg, probe)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusWarn {
		t.Fatalf("Run with daemon unreachable + job active = %+v, want StatusWarn", res)
	}
}

func TestDoctorCompletionGate_OKWhenDaemonReachable(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, fakeGate{ok: true}, fakeResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	probe := func(context.Context) (bool, bool, error) { return true, true, nil }
	check := hookpacks.NewCompletionGateDoctorCheck(reg, probe)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with daemon reachable = %+v, want StatusOK", res)
	}
}

func TestDoctorCompletionGate_Metadata(t *testing.T) {
	check := hookpacks.NewCompletionGateDoctorCheck(hookpacks.NewHookRegistry(), nil)
	if check.Name() == "" || check.Describe() == "" {
		t.Fatalf("Name/Describe must be non-empty: %q / %q", check.Name(), check.Describe())
	}
	if check.Metadata().Fixable {
		t.Fatalf("Metadata().Fixable = true, want false")
	}
	if _, err := check.Fix(context.Background()); err != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix err = %v, want ErrCheckNotFixable", err)
	}
}
