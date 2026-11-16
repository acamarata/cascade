// Package background contains guard state helpers for background supervision.
package background

// SilenceGuard describes whether the guard is silent.
type SilenceGuard string

const (
	// autonomousSilent indicates the supervisor is currently running.
	autonomousSilent SilenceGuard = "autonomous_silent"

	// guardNotSilent indicates the supervisor is not running.
	guardNotSilent SilenceGuard = "not_silent"
)

// GuardSilence is the guard silence value and equals autonomousSilent.
const GuardSilence = autonomousSilent

// GuardState returns autonomousSilent when supervisorRunning is true and
// guardNotSilent when supervisorRunning is false. Silence is therefore never
// reported without a running supervisor.
func GuardState(supervisorRunning bool) SilenceGuard {
	if supervisorRunning {
		return autonomousSilent
	}
	return guardNotSilent
}
