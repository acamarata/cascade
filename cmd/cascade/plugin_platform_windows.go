//go:build windows

// Purpose: the Windows (tier-2) half of the plugin CLI's platform gate.
// The daemon service is tier-2 (D/S-06.T2: no daemon socket on Windows at
// all) and the elevation helper (D/S-07.T6) is tier-2 refused, so every
// elevated plugin verb is refused HERE, before any daemon-required or
// CASCADE_NO_INPUT check runs, with the exact wording Art.5's acceptance
// criteria name.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import "github.com/acamarata/cascade/pkg/cascade"

// pluginWindowsTier2Refusal refuses elevatedKind ("daemon" for process-tier
// add/update, "elevation" for perms grant/revoke) with the acceptance
// criterion's exact message.
func pluginWindowsTier2Refusal(elevatedKind string) error {
	switch elevatedKind {
	case "elevation":
		return cascade.New(cascade.KindUnsupported, "elevation not available on Windows tier-2")
	default:
		return cascade.New(cascade.KindUnsupported, "daemon not available on Windows tier-2")
	}
}
