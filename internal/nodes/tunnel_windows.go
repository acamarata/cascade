//go:build windows

// Purpose: R-21.226's windows/amd64-native anchor for the controller-side
//
//	tunnel service refusal. RefuseTunnelServiceOnGOOS (tunnel.go) is a pure
//	function already unit-tested against the literal string "windows" on
//	every platform; this file exists only so a real windows/amd64 CI lane
//	compiles and runs a NATIVE assertion (tunnel_windows_test.go) against
//	the actual runtime.GOOS, mirroring serve.go's RefuseOnGOOS precedent
//	and daemon_windows.go's sibling-file split.
//
// SPORT: internal/nodes (P1-E17-W4-S36-T3, R-21.226).

package nodes

import "runtime"

// tunnelServiceGOOS is this build's runtime.GOOS, threaded through so
// tunnel_windows_test.go asserts against the real value rather than a
// hardcoded literal.
const tunnelServiceGOOS = runtime.GOOS
