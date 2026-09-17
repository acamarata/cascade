package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// stubDetector answers with fixed states or a fixed error.
type stubDetector struct {
	states []HarnessState
	err    error
}

func (s stubDetector) Detect(context.Context) ([]HarnessState, error) { return s.states, s.err }

// TestDoctorHarnessCheckStatuses walks every outcome the check reports,
// with the distinctions that matter for an exit code.
func TestDoctorHarnessCheckStatuses(t *testing.T) {
	installed := []HarnessState{
		{Kind: HarnessClaude, Detected: true, InstallPath: "/h/claude"},
		{Kind: HarnessCodex, Detected: false, InstallPath: "/h/.codex"},
		{Kind: HarnessOpenCode, Detected: false, InstallPath: "/h/.config/opencode"},
	}
	drifted := []HarnessState{
		{Kind: HarnessClaude, Detected: true, Drift: true, DriftReason: "missing on disk"},
		{Kind: HarnessCodex, Detected: false},
		{Kind: HarnessOpenCode, Detected: false},
	}
	none := []HarnessState{
		{Kind: HarnessClaude, Detected: false, InstallPath: "/h/claude"},
		{Kind: HarnessCodex, Detected: false, InstallPath: "/h/.codex"},
		{Kind: HarnessOpenCode, Detected: false, InstallPath: "/h/.config/opencode"},
	}

	for _, tc := range []struct {
		name   string
		states []HarnessState
		want   doctor.Status
	}{
		// Not having a harness is not a fault: a server install would
		// otherwise fail its own doctor for never having been a laptop.
		{"nothing installed is ok", none, doctor.StatusOK},
		{"installed and in sync is ok", installed, doctor.StatusOK},
		{"drift warns", drifted, doctor.StatusWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewHarnessCheck(stubDetector{states: tc.states}, nil).Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %v, want %v (message %q)", got.Status, tc.want, got.Message)
			}
			for _, kind := range SupportedHarnesses() {
				if !strings.Contains(got.Detail, string(kind)) {
					t.Errorf("detail omits %q: %q", kind, got.Detail)
				}
			}
		})
	}
}

// TestDoctorHarnessDriftNamesTheRemedy asserts a warning tells the
// operator what to run. A drift row with no remedy is a row they can only
// stare at.
func TestDoctorHarnessDriftNamesTheRemedy(t *testing.T) {
	got, err := NewHarnessCheck(stubDetector{states: []HarnessState{
		{Kind: HarnessClaude, Detected: true, Drift: true, DriftReason: "content differs"},
	}}, nil).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(got.Remediation, "context harness sync") {
		t.Fatalf("remediation = %q, want the sync command", got.Remediation)
	}
}

// TestDoctorHarnessSeparatesUnsupportedFromUnverifiable is the Art.1
// distinction. A platform this build does not resolve is a documented
// state, not a broken install; an environment nobody could read is a
// subject nobody looked at.
func TestDoctorHarnessSeparatesUnsupportedFromUnverifiable(t *testing.T) {
	unsupported, err := NewHarnessCheck(stubDetector{err: ErrHarnessDetectionUnsupported}, nil).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if unsupported.Status != doctor.StatusOK {
		t.Errorf("a tier-2 platform reported %v, want StatusOK naming the tier", unsupported.Status)
	}
	if !strings.Contains(unsupported.Message, "tier-2") {
		t.Errorf("message = %q, want it to name the tier", unsupported.Message)
	}

	broken, err := NewHarnessCheck(stubDetector{err: errors.New("HOME is unset")}, nil).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if broken.Status != doctor.StatusError {
		t.Fatalf("an unresolvable environment reported %v, want StatusError", broken.Status)
	}
}

// TestDoctorHarnessUsesItsDriftSource proves the source is consulted, by
// flipping a state the detector alone reports as clean.
func TestDoctorHarnessUsesItsDriftSource(t *testing.T) {
	detector := stubDetector{states: []HarnessState{{Kind: HarnessClaude, Detected: true}}}
	drift := func(context.Context) (SyncResult, error) {
		return SyncResult{Drift: []DriftResult{
			{Harness: "claude", Path: "/p/CLAUDE.md", Stale: true, Reason: "content differs"},
		}}, nil
	}
	got, err := NewHarnessCheck(detector, drift).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != doctor.StatusWarn {
		t.Fatalf("status = %v, want StatusWarn from the drift source", got.Status)
	}
}

// TestDoctorHarnessSurvivesAFailingDriftSource pins the separation: a
// broken drift check does not take detection down with it, because "is it
// installed" is still an answer worth having.
func TestDoctorHarnessSurvivesAFailingDriftSource(t *testing.T) {
	detector := stubDetector{states: []HarnessState{{Kind: HarnessClaude, Detected: true, InstallPath: "/h/c"}}}
	failing := func(context.Context) (SyncResult, error) { return SyncResult{}, errors.New("no context tree here") }

	got, err := NewHarnessCheck(detector, failing).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK — detection succeeded", got.Status)
	}
	if !strings.Contains(got.Detail, "in sync") {
		t.Errorf("detail = %q; an unmeasured drift column should not read as drifted", got.Detail)
	}
}

// TestDoctorHarnessCheckIsNotFixable pins the declaration against the
// behaviour. Drift HAS a remedy, and doctor's Fix runs without
// confirmation — regenerating a user's instruction files as a side effect
// of a diagnostic is a bigger action than a diagnostic should take.
func TestDoctorHarnessCheckIsNotFixable(t *testing.T) {
	check := NewHarnessCheck(stubDetector{}, nil)
	if check.Metadata().Fixable {
		t.Fatal("the check declares itself fixable")
	}
	if _, err := check.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix returned %v, want ErrCheckNotFixable", err)
	}
	if !check.Metadata().FirstRun {
		t.Error("the check is not tagged for a first run; it is a first run's first question")
	}
	if check.Name() != HarnessCheckName || check.Describe() == "" {
		t.Errorf("name = %q, describe = %q", check.Name(), check.Describe())
	}
}

// TestDoctorHarnessUnwiredReportsError covers the build-defect case.
func TestDoctorHarnessUnwiredReportsError(t *testing.T) {
	got, err := NewHarnessCheck(nil, nil).Run(context.Background())
	if err != nil || got.Status != doctor.StatusError {
		t.Fatalf("status = %v (err %v), want StatusError", got.Status, err)
	}
}
