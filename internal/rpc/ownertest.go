// Purpose: the one exported override of the daemon-owner UID that
//   guardLocalRequest compares every resolved socket peer against, so a test
//   outside this package (the composed-daemon route inventory in
//   cmd/cascade) can make its own real peer a non-owner without a second
//   OS user.
// Inputs: the running test's *testing.T and the UID to treat as the owner.
// Outputs: none; the override lasts until t's cleanups run, which restore
//   the previous value.
// Constraints: test-only. It takes a *testing.T so no production path can
//   call it with a value it could build, and its testonly-allow entry makes
//   the allow-list gate fail if a non-test caller ever appears. Call it
//   before the requests it should affect: the guard reads ownerUID on the
//   serving goroutine with no lock, and the socket write that carries each
//   request is what orders the two.
// SPORT: internal.rpc ownertest (WithOwnerUIDForTest) [ADD].

package rpc

import "testing"

// WithOwnerUIDForTest makes guardLocalRequest treat uid as the daemon
// owner until t ends, then restores the previous owner function.
func WithOwnerUIDForTest(t *testing.T, uid int) {
	t.Helper()
	prev := ownerUID
	ownerUID = func() int { return uid }
	t.Cleanup(func() { ownerUID = prev })
}
