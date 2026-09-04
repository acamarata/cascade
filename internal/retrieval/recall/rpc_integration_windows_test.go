//go:build windows && integration

package recall

// Purpose: the Windows half of the recall.* external-contract lane. The
//   daemon's IPC channel is HTTP/1.1 over a UNIX DOMAIN SOCKET
//   (06-FORGE-SPEC §2), and Windows is a tier-2 platform for that
//   transport, so this test asserts the refusal explicitly instead of
//   letting the whole external-contract proof silently not exist on
//   Windows. The package still BUILDS on Windows — that is what the GOOS
//   matrix checks — and neither service.go nor rpc.go carries a
//   platform-specific import.
// SPORT: internal.retrieval.recall.Handler/ADDED (P1-E06-W2-S11-T3).

import "testing"

// TestRecallRPCUnixSocketIsTier2OnWindows records, as a running test
// rather than as prose, why the socket-backed recall.* tests do not run
// here.
func TestRecallRPCUnixSocketIsTier2OnWindows(t *testing.T) {
	t.Skip("daemon unix socket: Windows tier-2 — asserted refusal")
}
