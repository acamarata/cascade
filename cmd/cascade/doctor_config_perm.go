// Purpose: the config-permissions doctor check: it reports what
//
//	runtime.CheckConfigPermissions says about config.toml, the SAME
//	classifier Load applies when it reads the file, so doctor and a daemon
//	start can never disagree about whether the file is acceptable.
//
// Inputs: a runtime.PathProvider (the config path) and the classifier, which
//
//	is injected so a test can feed each level without arranging file modes.
//
// Outputs: a doctor.Check named config-permissions: refuse is StatusError,
//
//	warn (group-writable, world-readable) is StatusWarn, ok is StatusOK, and
//	not_checked (windows, which has no unix mode bits) is StatusOK with a
//	"not checked" detail. A file that cannot be inspected is StatusError.
//
// Constraints: read-only; it never chmods the file (not Fixable).
// SPORT: cmd/cascade/doctor (ADD, config-permissions mount).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
)

// configPermClassifier classifies a config.toml path
// (runtime.CheckConfigPermissions in production).
type configPermClassifier func(path string) (runtime.ConfigPermFinding, error)

// configPermissionsCheck is the doctor.Check over a configPermClassifier.
type configPermissionsCheck struct {
	path     string
	classify configPermClassifier
}

// newConfigPermissionsDoctorCheck builds the check over the real classifier
// and the config path paths resolves.
func newConfigPermissionsDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return configPermissionsCheck{path: paths.ConfigPath(), classify: runtime.CheckConfigPermissions}
}

func (configPermissionsCheck) Name() string { return "config-permissions" }
func (configPermissionsCheck) Describe() string {
	return "config.toml is not writable or owned by anyone but its owner, and is not a hazard to read"
}
func (configPermissionsCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }
func (configPermissionsCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run classifies the config file and maps the level to a status.
func (c configPermissionsCheck) Run(context.Context) (doctor.CheckResult, error) {
	finding, err := c.classify(c.path)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "could not inspect config.toml permissions", Detail: err.Error()}, nil
	}
	switch finding.Level {
	case runtime.ConfigPermRefuse:
		return doctor.CheckResult{Status: doctor.StatusError, Message: "config.toml permissions are unsafe: Load refuses it",
			Detail: finding.Reason, Remediation: "restrict the file to its owner, e.g. chmod 600 " + c.path}, nil
	case runtime.ConfigPermWarn:
		return doctor.CheckResult{Status: doctor.StatusWarn, Message: "config.toml permissions are looser than recommended",
			Detail: finding.Reason, Remediation: "restrict the file to its owner, e.g. chmod 600 " + c.path}, nil
	case runtime.ConfigPermNotChecked:
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "config.toml permissions not checked on this platform",
			Detail: finding.Reason}, nil
	case runtime.ConfigPermOK:
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "config.toml permissions ok", Detail: finding.Reason}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusError, Message: "config.toml permission check returned an unknown level",
		Detail: string(finding.Level)}, nil
}
