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
	"errors"

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

// Run probes `rclone version` and reports one of three tiers, split by
// what was actually established rather than by whether the call returned
// an error:
//
//	StatusOK    a version was parsed — the target is usable.
//	StatusWarn  the binary is VERIFIED ABSENT — nothing is broken, the
//	            optional target is simply unavailable.
//	StatusError the subject could not be VERIFIED at all: a binary that
//	            exists but will not run, or output that will not parse.
//	            Art.1's "an unverifiable subject is never a silent OK"
//	            applies to exactly these, and not to a clean absence.
//
// None of the three is a check failure in the framework sense; the
// distinction lives entirely in the reported Status.
func (c RcloneDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	stdout, stderr, err := c.runner.Run(ctx, nil, "version")
	if errors.Is(err, ErrRcloneBinaryAbsent) {
		// VERIFIED ABSENT is not UNVERIFIABLE, and that distinction is the
		// whole reason this is StatusOK. Art.1 forbids reporting a subject
		// we COULD NOT VERIFY as a silent OK. An absent rclone is not that:
		// we verified it is not installed, and we know nothing is broken,
		// because rclone is one OPTIONAL target among fs and s3. The
		// Message says so out loud, so this is not a silent OK either.
		//
		// This shipped as StatusError and turned `cascade doctor` red on
		// all four CI platforms immediately, passing locally only because
		// the authoring agent had installed rclone for its own real-server
		// testing. StatusWarn was tried next and is ALSO wrong here, for a
		// non-obvious reason worth recording: cmd/cascade/doctor.go maps a
		// warn outcome to a NON-ZERO exit (doctor_test.go pins
		// warn -> ExitUnavailable deliberately), so a warn from a check
		// that runs on EVERY plain `cascade doctor` would still fail the
		// command on any machine without an optional tool. A doctor that
		// exits non-zero for "you could optionally install this" trains
		// operators to ignore it.
		//
		// KNOWN LIMITATION, deliberately not fixed here: this check has no
		// configuration awareness, so it cannot yet distinguish "rclone is
		// absent and unused" (fine, this branch) from "rclone is absent but
		// an rclone TARGET IS CONFIGURED" (genuinely broken, should be
		// StatusError). Wiring config in is a separate change; recorded in
		// T0-OPEN-FOLLOWUPS.
		//
		// Every OTHER failure below stays StatusError, because those really
		// are unverifiable: a binary that exists but will not run, and
		// output that will not parse, both leave us unable to say whether
		// the target works.
		return doctor.CheckResult{
			Status:      doctor.StatusOK,
			Message:     "rclone not installed; the optional rclone backup target is unavailable (fs and s3 are unaffected)",
			Detail:      string(stderr),
			Remediation: "install rclone (https://rclone.org/downloads/) if you intend to use the rclone backup target",
		}, nil
	}
	if err != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "rclone binary found but failed to run",
			Detail:      string(stderr),
			Remediation: "run `rclone version` manually; the binary is present but did not execute successfully",
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
