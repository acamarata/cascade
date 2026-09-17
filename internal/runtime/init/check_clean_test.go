package init

// Purpose (this file): the one thing `--check` exists to say — that there
//   is nothing left to do.
//
// WHY IT NEEDED ITS OWN FILE. Every existing --check test runs against a
//   VIRGIN home, where the plan is long and the exit code is 3 for half a
//   dozen real reasons. The case nobody covered is the converged one, and
//   that is the case the flag is for: an operator scripting on exit 3
//   reconverges forever if it can never be 0. It could not be, because
//   step 9 planned the first-run health check unconditionally — a check
//   that changes nothing, counted as a change (R-14.280, found by the W-4
//   hardening gate against the shipped artifact).
// SPORT: runtime/init --check clean verdict (ADD tests) — P1-E19-W4-S42-T7.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedDatabase puts a file where the local database goes, so a fixture
// claiming to be a converged machine is one.
func seedDatabase(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not really sqlite, but present"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCheckReportsNothingToDoOnAConvergedMachine is the assertion the
// defect failed: with every step's work already done or deliberately
// skipped, the plan is EMPTY.
func TestCheckReportsNothingToDoOnAConvergedMachine(t *testing.T) {
	// Nothing to wire, nothing to install: no harness detected, no
	// catalog entry, and the daemon skipped the way `--no-daemon` skips
	// it. What is left is the doctor step, which writes nothing.
	w, rec, out, home := fixture(t, Options{Mode: ModeCheck, NoDaemon: true}, nil)
	rec.detected = nil
	rec.entries = nil
	// A converged machine HAS its database. Leaving the file out made
	// this fixture describe a half-set-up machine while claiming to
	// describe a converged one, which is the only reason the plan it
	// expected to be empty could have been (R-14.280).
	seedDatabase(t, filepath.Join(home, "data", "cascade.db"))

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	if len(report.Diff) != 0 {
		t.Fatalf("--check plans %v on a machine with nothing outstanding; an operator scripting "+
			"on its exit code would reconverge forever", report.Diff)
	}
	// The doctor step still SAYS what it would do — the operator loses no
	// information, only the false claim that it is a pending change.
	if !strings.Contains(out.String(), "doctor") {
		t.Errorf("the doctor step vanished from the report entirely:\n%s", out)
	}
	if rec.doctorRan {
		t.Error("--check ran the health check")
	}
}

// TestCheckStillPlansRealWork is the other half, without which the test
// above would pass for a --check that planned nothing ever.
func TestCheckStillPlansRealWork(t *testing.T) {
	w, _, _, _ := fixture(t, Options{Mode: ModeCheck}, nil)
	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	if len(report.Diff) == 0 {
		t.Fatal("--check on a virgin home planned nothing")
	}
	joined := strings.Join(report.Diff, "\n")
	for _, want := range []string{"wire claude", "install the cascade daemon"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan omits %q:\n%s", want, joined)
		}
	}
	// And the health check is not among them: it is not a change.
	if strings.Contains(joined, "doctor") {
		t.Errorf("the plan counts the health check as a change:\n%s", joined)
	}
}

// TestCheckOnAVirginMachinePlansSomethingOnEveryPlatform is the half the
// converged test above cannot cover, and the half that broke.
//
// `--check` answers "would anything change?" from the PLAN. On Windows
// the daemon step is skipped outright (DaemonSupported is false), so once
// the doctor step stopped planning a health check that changes nothing,
// a virgin Windows machine — no cascade home, no database, nothing wired
// — planned NOTHING and `init --check` exited 0. "Nothing to do" about a
// machine with no cascade home at all (R-14.280).
//
// The platform is a parameter here precisely because the defect was
// platform-shaped and every other --check test runs on the fixture's
// darwin.
func TestCheckOnAVirginMachinePlansSomethingOnEveryPlatform(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			w, rec, _, _ := fixture(t, Options{Mode: ModeCheck}, nil)
			w.deps.GOOS = goos
			// Nothing to wire and nothing to install, so the only plan
			// entries left are the ones about this machine's own
			// existence: the cascade home and the database.
			rec.detected = nil
			rec.entries = nil

			report, err := w.Run(context.Background())
			if err != nil {
				t.Fatalf("Run(--check): %v", err)
			}
			if len(report.Diff) == 0 {
				t.Fatal("--check planned nothing on a machine with no cascade home and no database; " +
					"an operator scripting on exit 0 would believe it was set up")
			}
			joined := strings.Join(report.Diff, "\n")
			if !strings.Contains(joined, "create the local database") {
				t.Errorf("the plan does not mention creating the database:\n%s", joined)
			}
		})
	}
}
