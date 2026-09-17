//go:build integration

// Purpose (this file): the forwarded-socket echo helper the real-sshd
//   tunnel test round-trips through, split out of tunnel_test.go under the
//   300-line file cap.
// WHY IT IS ITS OWN THING: the helper retries a whole exchange rather than
//   a dial, which is a decision with a reason (sshd accepts on a remote
//   forward before the far side has wired it to anything), and a decision
//   with a reason deserves somewhere a reader can find it.
// SPORT: internal/nodes tunnel echo helper (ADD) — P1-E17-W4-S37-T7.

package nodes

import (
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// roundTripThroughRemoteSocket sends bytes through the forwarded socket
// and returns what came back.
//
// THE WHOLE EXCHANGE IS RETRIED, not just the dial. sshd accepts on a
// remote forward before the far side has finished wiring that forward to
// anything, so an early connection can be accepted and then reset — which
// is what this helper used to report as a hard failure ("read echo: ...
// connection reset by peer", CI 2026-09-17), on a lane where everything
// about the tunnel was in fact working. Retrying only the dial does not
// help: the dial is the part that already succeeded.
//
// A persistent failure is still a failure: the deadline is the same, and
// the LAST error is what gets reported rather than a generic timeout, so a
// genuinely broken forward reads as broken rather than as slow.
func roundTripThroughRemoteSocket(t *testing.T, remoteSock string) string {
	t.Helper()
	const msg = "hello-from-node"
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		got, err := tryEchoOnce(remoteSock, msg)
		if err == nil {
			return got
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no round trip through the forwarded socket before the deadline; last error: %v", last)
	return ""
}

// tryEchoOnce performs one dial-write-read against the forwarded socket.
func tryEchoOnce(remoteSock, msg string) (string, error) {
	conn, err := net.Dial("unix", remoteSock)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	// A read deadline, so a forward that accepts and then never answers
	// costs one interval rather than the whole budget.
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// TestTheEchoHelperFailsOnASocketThatNeverAnswers is the mutation proof
// for roundTripThroughRemoteSocket's retry loop: retrying a whole exchange
// is only safe if a single attempt can still FAIL, and fail promptly. A
// helper that hung on a forward which accepted and said nothing would turn
// a broken tunnel into a test timeout with no message.
func TestTheEchoHelperFailsOnASocketThatNeverAnswers(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "silent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Accepted and abandoned: exactly the shape sshd produces
			// while a remote forward is still being wired up.
			_ = conn.Close()
		}
	}()

	start := time.Now()
	if got, echoErr := tryEchoOnce(sock, "hello-from-node"); echoErr == nil {
		t.Fatalf("a socket that answers nothing reported a successful echo of %q", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("one attempt took %s; the read deadline did not fire", elapsed)
	}

	if _, echoErr := tryEchoOnce(filepath.Join(t.TempDir(), "absent.sock"), "x"); echoErr == nil {
		t.Error("a socket that does not exist reported a successful echo")
	}
}
