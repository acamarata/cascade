// Purpose: a nodes.Dialer/Session test fake that never opens a socket,
//
//	split out of node_test.go to stay under the 300-line cap.
//
// SPORT: cmd/cascade/node (test fakes, P1-E17-W4-S36-T4).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeVerifyOnlyDialer is a nodes.Dialer that never opens a socket: it
// runs verify(fingerprint) and returns its error, or a nil fake Session
// on success, exercising KnownHosts.Verify's fail-closed path for real.
type fakeVerifyOnlyDialer struct{ fingerprint string }

func (d fakeVerifyOnlyDialer) Dial(_ context.Context, _ nodes.Target, verify nodes.HostKeyVerifier) (nodes.Session, error) {
	if err := verify(d.fingerprint); err != nil {
		return nil, err
	}
	return fakeSession{}, nil
}

type fakeSession struct{}

func (fakeSession) ListenUnix(string) (nodes.Listener, error) {
	return nil, cascade.New(cascade.KindUnavailable, "not used")
}
func (fakeSession) Close() error          { return nil }
func (fakeSession) Done() <-chan struct{} { ch := make(chan struct{}); return ch }
