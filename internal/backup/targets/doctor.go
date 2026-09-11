// Purpose: the rclone version probe (§D-15), landed as a `cascade
// doctor` check (07 §doctor) so an operator sees whether the rclone
// backup target can be used before a backup actually needs it. This
// package's own composition-root registration site is
// cmd/cascade/doctor_mounts.go's productionCheckRegistry — NOT
// cmd/cascade/root.go, which this ticket's files_scope named but which
// carries no doctor-check registration code at all (R-16.79; the same
// wrong-path finding P1-E17-W4-S38-T6/T7's journals already recorded for
// the identical file).
//
// CONTRACT-VS-TREE: no `--backup` flag exists on `cascade doctor` today
// (doctorFlags in cmd/cascade/doctor.go carries only --first-run and
// --fix; NodesCheck's own precedent, internal/nodes/doctor.go, notes the
// identical gap for --nodes). This check therefore registers under the
// stable name "backup" and runs as part of every plain `cascade doctor`
// invocation, exactly as every other unfiltered check does, rather than
// behind a flag that does not exist.
//
// SPORT: internal.backup.targets.doctor/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
)

// BackupDoctorCheckName is this check's stable registry name.
const BackupDoctorCheckName = "backup"

// RcloneDoctorCheck is the doctor.Check that probes the rclone binary's
// presence and version.
type RcloneDoctorCheck struct {
	runner RcloneRunner
}

// NewRcloneDoctorCheck returns the check, using runner to invoke `rclone
// version`. A nil runner uses the real binary.
func NewRcloneDoctorCheck(runner RcloneRunner) doctor.Check {
	if runner == nil {
		runner = execRcloneRunner{}
	}
	return RcloneDoctorCheck{runner: runner}
}

// Name implements doctor.Check.
func (RcloneDoctorCheck) Name() string { return BackupDoctorCheckName }

// Describe implements doctor.Check.
func (RcloneDoctorCheck) Describe() string { return "the rclone backup target's binary and version" }

// Metadata implements doctor.Check.
func (RcloneDoctorCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Fix is not implemented: installing a missing binary is an operator
// action, not something this process should do unattended.
func (RcloneDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run probes `rclone version`. An absent binary or unparseable output is
// StatusError (Art.1: an unverifiable subject is never a silent OK) —
// only a successfully parsed version reports StatusOK. Both are
// legitimate outcomes of "the rclone target may or may not be usable
// here," so neither is a check failure in the framework sense, only in
// the reported Status.
func (c RcloneDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	stdout, stderr, err := c.runner.Run(ctx, nil, "version")
	if err != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "rclone binary not found or failed to run",
			Detail:      string(stderr),
			Remediation: "install rclone (https://rclone.org/downloads/) to use the rclone backup target",
		}, nil
	}
	v, perr := ParseRcloneVersionOutput(stdout)
	if perr != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "rclone version output could not be parsed",
			Detail:      perr.Error(),
			Remediation: "run `rclone version` manually and compare its output to this build's expectations",
		}, nil
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: "rclone " + v.Raw,
	}, nil
}
