//go:build windows

// Purpose: the Windows (tier-2) half of the bridge's platform gate. Windows
//   tier-2 has no daemon, so every Epic W bridge surface refuses with the
//   typed error the contract names verbatim (R-16.60a).
//
// Constraints: see module_unix.go's header for why this is a build-tag pair
//   and not a runtime.GOOS branch. The module makes no "runs headless on
//   Windows" claim anywhere.
//
// SPORT: plugins/cascade-pa/telegram platformBridgeRefusal/ADDED
//   (P1-E23-W5-S48-T1).

package telegram

// platformBridgeRefusal returns the tier-2 refusal on Windows.
func platformBridgeRefusal() error { return errWindowsTier2 }
