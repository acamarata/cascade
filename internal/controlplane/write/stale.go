package write

import "time"

// StaleFinding reports that a supervisor is stale relative to the installed
// binary.
type StaleFinding struct {
	// Kind identifies the finding category.
	Kind string
	// Code is the stable error code for this finding.
	Code string
}

// StaleThreshold is the minimum age difference between the supervisor start
// time and the binary build time that produces a stale-binary finding.
const StaleThreshold = 30 * time.Minute

// CheckStaleBinary compares supervisorStart with binaryBuild. It returns a
// supervisor_stale_binary finding and true when the binary was built at least
// StaleThreshold after the supervisor started; otherwise it returns a zero
// finding and false.
func CheckStaleBinary(supervisorStart, binaryBuild time.Time) (StaleFinding, bool) {
	if binaryBuild.Sub(supervisorStart) >= StaleThreshold {
		return StaleFinding{
			Kind: "supervisor_stale_binary",
			Code: "D1081",
		}, true
	}
	return StaleFinding{}, false
}
