// Purpose: RcloneDoctorCheck tests against a recording fakeRunner (no
// process spawn) plus one real-binary run gated by rclone's actual
// presence (skipped, not faked, when absent).
//
// SPORT: internal.backup.targets.doctor/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/doctor"
)

func TestRcloneDoctorCheck_Metadata(t *testing.T) {
	c := targets.NewRcloneDoctorCheck(&fakeRunner{})
	if c.Name() != targets.BackupDoctorCheckName {
		t.Fatalf("Name() = %q, want %q", c.Name(), targets.BackupDoctorCheckName)
	}
	if c.Metadata().Fixable {
		t.Fatal("Metadata().Fixable = true, want false")
	}
}

func TestRcloneDoctorCheck_FixReturnsNotFixable(t *testing.T) {
	c := targets.NewRcloneDoctorCheck(&fakeRunner{})
	_, err := c.Fix(context.Background())
	if !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix() error = %v, want ErrCheckNotFixable", err)
	}
}

func TestRcloneDoctorCheck_BinaryPresentReportsOK(t *testing.T) {
	r := &fakeRunner{stdout: []byte("rclone v1.75.1\n- os/type: darwin\n")}
	c := targets.NewRcloneDoctorCheck(r)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK; message: %s detail: %s", res.Status, res.Message, res.Detail)
	}
}

// TestRcloneDoctorCheck_BinaryAbsentReportsOK pins the tier for a
// VERIFIED-ABSENT optional binary at OK — stated, never silent.
//
// This originally asserted StatusError, citing Art.1's "an absent binary is
// never a silent OK". The citation was right; the tier was wrong twice
// over. As StatusError the check failed `cascade doctor` on all four CI
// platforms the moment it shipped, passing locally only because the
// authoring agent had installed rclone for its own real-server testing.
// StatusWarn was then tried and is also wrong, for a reason that is easy to
// miss: cmd/cascade maps a warn outcome to a NON-ZERO exit (doctor_test.go
// pins warn -> ExitUnavailable deliberately), so a warn from a check that
// runs on every plain `cascade doctor` still fails the command on any
// machine lacking an optional tool.
//
// This is a BEHAVIOUR fix, not a test relaxed to fit the code. Two things
// keep it from becoming the silent pass Art.1 forbids: the assertions below
// require the absence to be stated in Message and actionable via
// Remediation, and the sibling test pins StatusError for the case that is
// genuinely unverifiable — so this is a split, not a downgrade.
func TestRcloneDoctorCheck_BinaryAbsentReportsOK(t *testing.T) {
	r := &fakeRunner{err: targets.ErrRcloneBinaryAbsent}
	c := targets.NewRcloneDoctorCheck(r)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK (absent OPTIONAL target is not a broken install)", res.Status)
	}
	// Not a SILENT ok: Art.1's objection is to an unverifiable subject
	// passing quietly. Both of these must hold, or this really would be one.
	if res.Message == "" || !strings.Contains(res.Message, "rclone") {
		t.Fatalf("Message = %q, want it to state plainly that rclone is absent", res.Message)
	}
	if res.Remediation == "" {
		t.Fatal("an absent-tool OK must still carry a remediation, or it is an unactionable silent pass")
	}
}

// TestRcloneDoctorCheck_BinaryPresentButFailsReportsError proves the tier
// split above is real: a binary that EXISTS but will not run leaves the
// subject genuinely unverifiable, which is exactly what Art.1 reserves
// StatusError for. Without this test, the Warn change above could not be
// distinguished from having weakened the check across the board.
func TestRcloneDoctorCheck_BinaryPresentButFailsReportsError(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 1: permission denied")}
	c := targets.NewRcloneDoctorCheck(r)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError (present-but-unrunnable is unverifiable)", res.Status)
	}
}

func TestRcloneDoctorCheck_UnparseableOutputReportsError(t *testing.T) {
	r := &fakeRunner{stdout: []byte("not rclone output")}
	c := targets.NewRcloneDoctorCheck(r)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError", res.Status)
	}
}

// TestRcloneDoctorCheck_RealBinary runs the check against the REAL
// production runner (execRcloneRunner, via a nil runner) — the Art.2
// real counterpart for the version probe itself, skipped rather than
// faked when rclone is not on this machine's PATH.
func TestRcloneDoctorCheck_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not on PATH; skipping real-binary probe")
	}
	c := targets.NewRcloneDoctorCheck(nil)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK against a real installed rclone; detail: %s", res.Status, res.Detail)
	}
}

func TestRcloneDoctorCheck_Describe(t *testing.T) {
	c := targets.NewRcloneDoctorCheck(&fakeRunner{})
	if c.Describe() == "" {
		t.Fatal("Describe() returned empty string")
	}
}
