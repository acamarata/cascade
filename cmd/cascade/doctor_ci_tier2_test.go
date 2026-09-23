// Purpose: tests for cmd/cascade/doctor_ci_tier2.go — pins the
//
//	GOOS-switched informational verdict (never warn/fail) and that the
//	real productionCheckRegistry actually mounts it. Deleting the
//	Register call in doctor_mounts.go turns TestDoctorRegistersCITier2
//	red.
//
// Constraints: never depends on the test binary's own runtime.GOOS —
//
//	both platform branches are exercised by constructing ciTier2Check
//	directly with a fixed goos, per the ticket's platform-seam design.
//
// SPORT: cmd/cascade doctor_ci_tier2.go (ADD) — P1-E25-W5-S97-T1.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// TestDoctorCITier2InformationalOnWindows pins the Windows branch: status
// ok, never warn or fail, with the exact detail text naming both verbs.
func TestDoctorCITier2InformationalOnWindows(t *testing.T) {
	check := ciTier2Check{goos: "windows"}
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run on windows = status %q, want %q (tier-2 refusal is specified behaviour, never warn/fail)",
			res.Status, doctor.StatusOK)
	}
	for _, verb := range []string{"cascade github ci wait", "cascade github ci watch add"} {
		if !strings.Contains(res.Message, verb) {
			t.Fatalf("Run on windows message = %q, want it to name %q", res.Message, verb)
		}
	}
	if !strings.Contains(res.Message, "tier-2") || !strings.Contains(res.Message, "daemon") {
		t.Fatalf("Run on windows message = %q, want it to explain the tier-2/no-daemon refusal", res.Message)
	}
}

// TestDoctorCITier2OKOnUnix pins the darwin/linux branch: status ok.
func TestDoctorCITier2OKOnUnix(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			check := ciTier2Check{goos: goos}
			res, err := check.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Status != doctor.StatusOK {
				t.Fatalf("Run on %s = status %q, want %q", goos, res.Status, doctor.StatusOK)
			}
		})
	}
}

// TestDoctorCITier2_ContextDone pins the Art.1 discipline every check in
// this package shares: a context already done before Run is StatusError,
// never a silent OK.
func TestDoctorCITier2_ContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := (ciTier2Check{goos: "linux"}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run with a done context = status %q, want %q", res.Status, doctor.StatusError)
	}
}

// TestDoctorCITier2_NotFixable pins Fix's refusal: tier-2 refusal is
// specified behaviour, not something --fix could remediate.
func TestDoctorCITier2_NotFixable(t *testing.T) {
	_, err := (ciTier2Check{goos: "windows"}).Fix(context.Background())
	if err != doctor.ErrCheckNotFixable {
		t.Fatalf("Fix error = %v, want doctor.ErrCheckNotFixable", err)
	}
}

// TestDoctorRegistersCITier2 drives the REAL productionCheckRegistry and
// asserts ci_tier2 is present on it.
func TestDoctorRegistersCITier2(t *testing.T) {
	useTempCustody(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, check := range reg.List() {
		if check.Name() == "ci_tier2" {
			return
		}
	}
	t.Fatal("ci_tier2 is not registered on the real productionCheckRegistry")
}
