//go:build windows

// Purpose: the Windows (tier-2) half of wait-on-green's platform gate.
// Windows tier-2 has no daemon; a future `cascade github ci wait`
// entrypoint refuses with this typed error rather than attempting a poll
// loop tier-2 cannot support.
//
// Constraints: see waitmerge_unix.go's header for why this is a build-tag
// pair and not a runtime.GOOS branch.
//
// SPORT: internal.ci.waitDaemonAbsentRefusal/ADDED (P1-E25-W5-S51-T3).

package ci

import "github.com/acamarata/cascade/pkg/cascade"

// errWaitWindowsTier2 is the typed, exact refusal this platform returns.
var errWaitWindowsTier2 = cascade.New(cascade.KindUnsupported,
	"ci: cascade github ci wait requires the daemon (Windows tier-2)")

// waitDaemonAbsentRefusal reports the tier-2 refusal on Windows.
func waitDaemonAbsentRefusal() error { return errWaitWindowsTier2 }
