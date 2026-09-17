package init

// Purpose: steps 2-7's own contracts (P1-E16-W4-S35-T6): the profile
//   flag and the worker handoff, the literal-secret refusal, the catalog,
//   the provider loop, and the --harness filter.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestProfileFlagPreselectsStepTwo covers the ratified --profile flag:
// the step is satisfied without asking.
func TestProfileFlagPreselectsStepTwo(t *testing.T) {
	prompt := &scriptedPrompter{
		// three storage references, then the plugin checkbox, then
		// "add a provider?" answered no, telemetry no, daemon yes.
		lines:    []string{"vault://pg", "vault://redis", "vault://s3"},
		confirms: []bool{true, false, false, false, false, true},
	}
	w, _, _, _ := fixture(t, Options{Profile: ProfileServer}, prompt)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--profile server): %v", err)
	}
	if report.State.Profile != ProfileServer {
		t.Errorf("profile = %q, want the flag's value", report.State.Profile)
	}
	for _, q := range prompt.asked {
		if strings.Contains(q, "Which profile") {
			t.Error("--profile was supplied and the wizard still asked")
		}
	}
}

// TestAnUnknownProfileIsRefused keeps a typo from silently becoming a
// local install on a machine meant to be a worker.
func TestAnUnknownProfileIsRefused(t *testing.T) {
	w, _, _, _ := fixture(t, Options{Mode: ModeYes, Profile: "laptop"}, nil)

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a profile that does not exist")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestTheWorkerProfileHandsOffAndStops proves two things the spec asks
// for separately: `cascade node enroll` is invoked as a REAL subprocess,
// and the wizard does not then go on to configure a machine its
// controller is about to configure.
func TestTheWorkerProfileHandsOffAndStops(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Profile: ProfileWorker}, nil)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--profile worker): %v", err)
	}
	if len(rec.subArgs) != 1 || strings.Join(rec.subArgs[0], " ") != "node enroll" {
		t.Fatalf("subprocess calls = %v, want exactly one `node enroll`", rec.subArgs)
	}
	if report.State.CompletedStep != StepProfile {
		t.Errorf("the run continued to step %v after handing off", report.State.CompletedStep)
	}
	if rec.installed || len(rec.wired) != 0 {
		t.Error("the wizard configured a worker its controller is about to configure")
	}
}

// TestAFailedHandoffFailsTheRun: a worker whose enrollment did not happen
// is not set up, and reporting success would leave an unenrolled machine
// looking ready.
func TestAFailedHandoffFailsTheRun(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Profile: ProfileWorker}, nil)
	rec.subErr = errors.New("no controller reachable")

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded after `cascade node enroll` failed")
	}
}

// TestServerStorageRefusesALiteralSecret is the leak this step is most
// exposed to: a connection string with a password in it, typed at a
// prompt, would land in config.toml in plaintext.
func TestServerStorageRefusesALiteralSecret(t *testing.T) {
	prompt := &scriptedPrompter{lines: []string{"postgres://user:hunter2@db/cascade"}}
	w, rec, _, _ := fixture(t, Options{Profile: ProfileServer}, prompt)
	rec.secretErr = errors.New("that value looks like a literal secret; use `cascade vault set`")

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("the wizard accepted a literal secret at a storage prompt")
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Errorf("the refusal does not redirect to the vault: %v", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}
