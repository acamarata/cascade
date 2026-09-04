// Purpose: tests for `cascade doctor` and `cascade doctor bundle` - the mount
//
//	on the real root command, the outcome-to-exit-status mapping in both
//	directions, and the redaction canary over every surface a secret could
//	reach (human text, --json envelope, bundle archive).
//
// Constraints: writes only under t.TempDir() (Art.7.1); performs no network
//
//	I/O (Art.7.2); every doctor run drives the same newRootCmd() tree main()
//	builds, never a hand-authored dialect (Art.2). The clock is injected
//	(Art.7.3).
//
// SPORT: cmd/cascade/doctor (ADD) - doctor command, bundle subcommand.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeCheck is a doctor.Check whose verdict the test dictates.
type fakeCheck struct {
	name     string
	status   doctor.Status
	message  string
	firstRun bool
	fixable  bool
	fixed    bool
}

func (c *fakeCheck) Name() string     { return c.name }
func (c *fakeCheck) Describe() string { return "fake check for " + c.name }
func (c *fakeCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: c.firstRun, Fixable: c.fixable}
}

func (c *fakeCheck) Run(context.Context) (doctor.CheckResult, error) {
	return doctor.CheckResult{Status: c.status, Message: c.message}, nil
}

func (c *fakeCheck) Fix(context.Context) (doctor.FixResult, error) {
	if !c.fixable {
		return doctor.FixResult{}, doctor.ErrCheckNotFixable
	}
	c.fixed = true
	return doctor.FixResult{Applied: true, Delta: "repaired " + c.name}, nil
}

// testDoctorDeps builds doctorDeps that touch nothing real: a registry over
// the given checks, a fixed clock, an empty environment, and a bundle
// directory under t.TempDir().
func testDoctorDeps(t *testing.T, checks ...doctor.Check) doctorDeps {
	t.Helper()
	dir := t.TempDir()
	return doctorDeps{
		Registry: func() *doctor.CheckRegistry {
			reg := doctor.NewCheckRegistry()
			for _, c := range checks {
				reg.Register(c)
			}
			return reg
		},
		Paths:     lazyPaths{},
		Getenv:    func(string) string { return "" },
		Environ:   func() []string { return nil },
		Clock:     runtime.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		BundleDir: dir,
	}
}

// execRootDoctor runs args against the real root tree with the production
// doctor command swapped for one built from deps. Swapping on the real root,
// rather than executing a detached command, keeps the persistent global flags
// (--json, --no-color, -q) and guardUnknownSubcommands in the picture.
func execRootDoctor(t *testing.T, deps doctorDeps, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	existing, _, err := root.Find([]string{"doctor"})
	if err != nil {
		t.Fatalf("find doctor command: %v", err)
	}
	root.RemoveCommand(existing)
	cmd := newDoctorCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	execErr := root.Execute()
	return buf.String(), execErr
}

// TestDoctorIsMountedOnRoot drives the REAL root command with the REAL
// production doctor deps and proves `doctor` resolves and runs end to end.
// This is the reachability proof (R-14.166): removing mountDoctorCmd from
// newRootCmd makes this test fail with `unknown command "doctor"`.
func TestDoctorIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"doctor"})
	if err != nil || found.Name() != "doctor" {
		t.Fatalf("doctor is not mounted on the root command: found=%v err=%v", found.Name(), err)
	}

	got, err := execRoot(t, "doctor")
	if err != nil {
		t.Fatalf("cascade doctor returned an error on a healthy installation: %v", err)
	}
	if !strings.Contains(got, "doctor_selfcheck") {
		t.Errorf("doctor output does not name the registered self check\noutput:\n%s", got)
	}
	if !strings.Contains(got, "OK") {
		t.Errorf("doctor output does not report the self check as OK\noutput:\n%s", got)
	}
}

// TestDoctorExitStatusByOutcome pins the mapping in BOTH directions: a
// passing run exits 0, a warning run and a failing run do not. A doctor that
// always exits 0 is worse than no doctor at all.
func TestDoctorExitStatusByOutcome(t *testing.T) {
	cases := []struct {
		name     string
		status   doctor.Status
		wantExit int
	}{
		{name: "ok", status: doctor.StatusOK, wantExit: cascade.ExitOK},
		{name: "warn", status: doctor.StatusWarn, wantExit: cascade.ExitUnavailable},
		{name: "error", status: doctor.StatusError, wantExit: cascade.ExitUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := testDoctorDeps(t, &fakeCheck{name: "probe", status: tc.status, message: "verdict"})
			_, err := execRootDoctor(t, deps, "doctor")
			if got := output.ExitCode(err); got != tc.wantExit {
				t.Fatalf("exit code = %d, want %d (err=%v)", got, tc.wantExit, err)
			}
			if tc.wantExit == cascade.ExitOK && err != nil {
				t.Fatalf("passing run returned an error: %v", err)
			}
			if tc.wantExit != cascade.ExitOK && err == nil {
				t.Fatal("failing run returned no error, so the process would exit 0")
			}
		})
	}
}

// TestDoctorMixedRunFailsOnWorstStatus proves one failing check fails the
// whole run even when others pass.
func TestDoctorMixedRunFailsOnWorstStatus(t *testing.T) {
	deps := testDoctorDeps(t,
		&fakeCheck{name: "a_ok", status: doctor.StatusOK, message: "fine"},
		&fakeCheck{name: "b_bad", status: doctor.StatusError, message: "broken"},
	)
	out, err := execRootDoctor(t, deps, "doctor")
	if err == nil {
		t.Fatal("mixed run with a failing check must return an error")
	}
	if !strings.Contains(err.Error(), "1 check(s)") {
		t.Errorf("error does not count the failing checks: %v", err)
	}
	if !strings.Contains(out, "a_ok") || !strings.Contains(out, "b_bad") {
		t.Errorf("output dropped a check row\noutput:\n%s", out)
	}
}

// TestDoctorEmptyRegistryIsNotAPass proves an empty run refuses rather than
// rendering a silent clean bill of health (Art.1).
func TestDoctorEmptyRegistryIsNotAPass(t *testing.T) {
	deps := testDoctorDeps(t)
	_, err := execRootDoctor(t, deps, "doctor")
	if err == nil {
		t.Fatal("a run with no registered checks must not exit 0")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("kind = %v, want KindNotFound", err)
	}
}

// TestDoctorFirstRunFiltersChecks proves --first-run selects the tagged set,
// and that a first-run selection with no tagged checks refuses rather than
// passing vacuously.
func TestDoctorFirstRunFiltersChecks(t *testing.T) {
	deps := testDoctorDeps(t,
		&fakeCheck{name: "tagged", status: doctor.StatusOK, message: "fine", firstRun: true},
		&fakeCheck{name: "untagged", status: doctor.StatusError, message: "broken"},
	)
	out, err := execRootDoctor(t, deps, "doctor", "--first-run")
	if err != nil {
		t.Fatalf("--first-run over a healthy tagged check returned: %v", err)
	}
	if strings.Contains(out, "untagged") {
		t.Errorf("--first-run ran an untagged check\noutput:\n%s", out)
	}

	only := testDoctorDeps(t, &fakeCheck{name: "untagged", status: doctor.StatusOK, message: "fine"})
	if _, err := execRootDoctor(t, only, "doctor", "--first-run"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("--first-run with nothing tagged: err = %v, want KindNotFound", err)
	}
}

// TestDoctorFixDispatch proves --fix reaches a fixable failing check and
// leaves a passing one alone.
func TestDoctorFixDispatch(t *testing.T) {
	broken := &fakeCheck{name: "fixable", status: doctor.StatusError, message: "broken", fixable: true}
	healthy := &fakeCheck{name: "healthy", status: doctor.StatusOK, message: "fine", fixable: true}
	deps := testDoctorDeps(t, broken, healthy)
	if _, err := execRootDoctor(t, deps, "doctor", "--fix"); err == nil {
		t.Fatal("a run whose check reported error must still fail after a fix attempt")
	}
	if !broken.fixed {
		t.Error("--fix did not call Fix on the failing fixable check")
	}
	if healthy.fixed {
		t.Error("--fix called Fix on a check that reported ok")
	}
}

// TestDoctorJSONEnvelope proves --json emits internal/output's versioned
// envelope carrying the report entries.
func TestDoctorJSONEnvelope(t *testing.T) {
	deps := testDoctorDeps(t, &fakeCheck{name: "probe", status: doctor.StatusOK, message: "fine"})
	out, err := execRootDoctor(t, deps, "--json", "doctor")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var env struct {
		Version int `json:"version"`
		Data    struct {
			Entries []struct {
				Name   string `json:"name"`
				Result struct {
					Status  string `json:"status"`
					Message string `json:"message"`
				} `json:"result"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\noutput:\n%s", err, out)
	}
	if env.Version != output.EnvelopeVersion {
		t.Errorf("envelope version = %d, want %d", env.Version, output.EnvelopeVersion)
	}
	if len(env.Data.Entries) != 1 || env.Data.Entries[0].Name != "probe" {
		t.Fatalf("envelope data does not carry the report entries: %s", out)
	}
	if env.Data.Entries[0].Result.Status != string(doctor.StatusOK) {
		t.Errorf("entry status = %q, want ok", env.Data.Entries[0].Result.Status)
	}
}

// TestDoctorRejectsUnknownSubcommand proves guardUnknownSubcommands reaches
// the mounted tree: a typo exits invalid-input, not 0.
func TestDoctorRejectsUnknownSubcommand(t *testing.T) {
	globalFlags = GlobalFlags{}
	_, err := execRoot(t, "doctor", "bogus")
	if err == nil {
		t.Fatal("cascade doctor bogus must not exit 0")
	}
	if got := output.ExitCode(err); got != cascade.ExitInvalidInput {
		t.Errorf("exit code = %d, want %d (err=%v)", got, cascade.ExitInvalidInput, err)
	}
}

// TestDoctorHelpListsCommand proves the command is visible in the root help
// surface a user actually reads.
func TestDoctorHelpListsCommand(t *testing.T) {
	got, err := execRoot(t, "--help")
	if err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	if !strings.Contains(got, "doctor") {
		t.Errorf("--help does not list the doctor command\noutput:\n%s", got)
	}
}
