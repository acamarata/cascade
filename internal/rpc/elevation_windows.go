//go:build windows

package rpc

// platformElevationRefusal implements this ticket's Windows tier-2
// requirement: on Windows, an elevated verb ALWAYS returns
// ELEVATION_REQUIRED with a message directing the operator to a POSIX
// platform, never runs the attestation flow at all. The in-process
// daemonless-elevation path referenced by the contract is wired by
// D/S-07.T4, not this ticket.
func platformElevationRefusal() *ErrorObject {
	return &ErrorObject{
		Code:    codeElevationRequired,
		Message: "elevation is not supported on Windows (tier-2); run this operation from a POSIX daemon (macOS or Linux) instead",
		Data:    elevationRequiredData{Reason: "ELEVATION_REQUIRED"},
	}
}
