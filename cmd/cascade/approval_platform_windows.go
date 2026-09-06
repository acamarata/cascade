//go:build windows

// Purpose: the Windows (tier-2) half of the approval CLI's platform gate.
//
//	No elevation flow exists on Windows at all, so an elevated verb is
//	refused here, before the socket is dialled, with the platform-
//	unsupported kind rather than a silent success or a panic.
//
// SPORT: cli/approval-standing/ADD (P1-E09-W2-S18-T6).
package main

import "github.com/acamarata/cascade/pkg/cascade"

// refuseElevatedOnUnsupportedPlatform always refuses on Windows and names
// the verb that was refused, so the message says what to run elsewhere.
func refuseElevatedOnUnsupportedPlatform(verb string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"cascade %s is not supported on this platform: elevated approval verbs need the "+
			"local attestation flow, which exists only on macOS and Linux", verb)
}
