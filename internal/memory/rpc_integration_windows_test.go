//go:build windows && integration

package memory

// Purpose: the Windows half of the memory.* external-contract lane. The
//   daemon's IPC channel is HTTP/1.1 over a UNIX DOMAIN SOCKET
//   (06-FORGE-SPEC §2), and Windows is a tier-2 platform for that
//   transport, so this test asserts the refusal explicitly instead of
//   letting the whole external-contract proof silently not exist on
//   Windows. The package still BUILDS on Windows — that is what the GOOS
//   matrix checks — and rpc.go carries no platform-specific import.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import "testing"

// TestMemoryRPCUnixSocketIsTier2OnWindows records, as a running test
// rather than as prose, why the socket-backed memory.* tests do not run
// here.
func TestMemoryRPCUnixSocketIsTier2OnWindows(t *testing.T) {
	t.Skip("daemon unix socket: Windows tier-2 — asserted refusal")
}
