//go:build !(darwin && cgo)

// Purpose: the caller check of builds without Security.framework: there is
//
//	no way to verify a peer, so none is accepted (kind unavailable, the same
//	answer the unavailable backend gives).
//
// SPORT: cmd/cascade-elevate-helper callerCheck/ADDED (P1-SHIP-15).
package main

// bindCaller refuses every caller: there is no way to verify a peer.
func bindCaller(int) (func() error, error) {
	return func() error { return errUnavailable() }, nil
}
