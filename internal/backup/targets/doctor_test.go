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

func TestRcloneDoctorCheck_BinaryAbsentReportsError(t *testing.T) {
	r := &fakeRunner{err: targets.ErrRcloneBinaryAbsent}
	c := targets.NewRcloneDoctorCheck(r)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Status = %v, want StatusError (Art.1: absent binary is never a silent OK)", res.Status)
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
