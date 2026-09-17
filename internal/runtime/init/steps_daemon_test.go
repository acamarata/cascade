package init

// Purpose: step 8's platform branches and step 9's summary
//   (P1-E16-W4-S35-T6).
// Constraints: every branch is driven by setting Deps.GOOS, so all three
//   platforms are covered on any host. The windows case ALSO runs
//   natively in the CI windows lane — but it is not gated behind a build
//   constraint, because a test that only runs on one platform is a test
//   that does not run in most reviews.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWizardDaemonStepPerPlatform drives step 8 on every platform this
// build compiles for, over the injected GOOS seam.
func TestWizardDaemonStepPerPlatform(t *testing.T) {
	for _, tc := range []struct {
		goos          string
		wantInstalled bool
		wantEnrolled  bool
	}{
		{"darwin", true, true},
		{"linux", true, true},
		{GOOSWindows, false, false},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
			w.deps.GOOS = tc.goos

			report, err := w.Run(context.Background())
			if err != nil {
				t.Fatalf("Run on %s: %v", tc.goos, err)
			}
			if rec.installed != tc.wantInstalled {
				t.Errorf("installed = %v on %s, want %v", rec.installed, tc.goos, tc.wantInstalled)
			}
			if rec.enrolled != tc.wantEnrolled {
				t.Errorf("enrolled = %v on %s, want %v", rec.enrolled, tc.goos, tc.wantEnrolled)
			}
			if !report.State.Complete() {
				t.Errorf("the run did not finish on %s; a platform without a service manager "+
					"has not had init go wrong", tc.goos)
			}
			if tc.wantInstalled {
				return
			}
			if report.State.DaemonSkipReason == "" {
				t.Error("step 8 installed nothing and recorded no reason, so the summary has nothing to say")
			}
			if !strings.Contains(out.String(), "tier-2") {
				t.Errorf("no tier-2 message was emitted on %s:\n%s", tc.goos, out)
			}
		})
	}
}

// TestWizardDaemonWindowsTier2Refusal is the named windows assertion. It
// runs on every host through the seam, and additionally in the CI windows
// lane, where Deps.GOOS is that runner's own platform.
//
// The assertion is that init SUCCEEDS: a tier-2 platform is one where the
// step does not apply, not one where the wizard fails.
func TestWizardDaemonWindowsTier2Refusal(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
	w.deps.GOOS = GOOSWindows

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("init failed on a tier-2 platform: %v", err)
	}
	if rec.installed {
		t.Error("a service was installed on a platform with no service manager")
	}
	if rec.enrolled {
		t.Error("the elevation helper was enrolled on a platform where elevation is refused")
	}
	if !report.State.Complete() {
		t.Error("init did not complete on a tier-2 platform")
	}
	if !strings.Contains(report.State.DaemonSkipReason, "tier-2") {
		t.Errorf("the recorded skip reason is %q, want it to name tier-2", report.State.DaemonSkipReason)
	}
	if !strings.Contains(out.String(), "cascade daemon run") {
		t.Errorf("the tier-2 message does not tell the operator what to do instead:\n%s", out)
	}
	if DaemonSupported(GOOSWindows) {
		t.Error("DaemonSupported says this platform has a service manager")
	}
}

// TestNoDaemonFlagSkipsStepEight covers the ratified --no-daemon flag,
// and pins that its reason is DIFFERENT from the tier-2 one: an operator
// who chose to skip and a platform that cannot are not the same fact, and
// the summary must not print the same sentence for both.
func TestNoDaemonFlagSkipsStepEight(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, NoDaemon: true}, nil)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--no-daemon): %v", err)
	}
	if rec.installed {
		t.Error("--no-daemon still installed the daemon")
	}
	if report.State.DaemonSkipReason != "--no-daemon" {
		t.Errorf("skip reason = %q, want it to name the flag", report.State.DaemonSkipReason)
	}
}

// TestHelperEnrollmentFailureDoesNotFailInit: the daemon is installed and
// the machine is usable; what the operator loses is the elevated verbs,
// and telling them so beats unwinding a successful install.
func TestWizardHelperEnrollment(t *testing.T) {
	t.Run("the fingerprint reaches the summary", func(t *testing.T) {
		w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
		report, err := w.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.State.HelperFingerprint != rec.fingerprint {
			t.Errorf("journal fingerprint = %q, want %q", report.State.HelperFingerprint, rec.fingerprint)
		}
		if !strings.Contains(out.String(), rec.fingerprint) {
			t.Errorf("the summary card omits the fingerprint:\n%s", out)
		}
	})

	t.Run("a failure is reported, not fatal", func(t *testing.T) {
		w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
		rec.enrollErr = cascade.New(cascade.KindUnavailable, "the trust store is locked")

		report, err := w.Run(context.Background())
		if err != nil {
			t.Fatalf("an enrollment failure failed the whole run: %v", err)
		}
		if !rec.installed {
			t.Error("the daemon install was unwound by an unrelated enrollment failure")
		}
		if !report.State.Complete() {
			t.Error("the run did not finish")
		}
		if !strings.Contains(out.String(), "elevated verbs will refuse") {
			t.Errorf("the operator was not told what the failure costs them:\n%s", out)
		}
	})
}

// TestTheSummaryNamesEverySkipReason keeps step 9 from printing a blank
// line for a step that did not run.
func TestTheSummaryNamesEverySkipReason(t *testing.T) {
	for reason, want := range map[string]string{
		"--no-daemon":      "not installed (--no-daemon)",
		tier2DaemonMessage: "not installed (" + tier2DaemonMessage + ")",
		"declined":         "not installed (declined)",
	} {
		if got := daemonLine(State{DaemonSkipReason: reason}); got != want {
			t.Errorf("daemonLine(%q) = %q, want %q", reason, got, want)
		}
	}
	if got := daemonLine(State{DaemonInstalled: true}); got != "installed" {
		t.Errorf("daemonLine(installed) = %q", got)
	}
}
