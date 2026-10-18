//go:build integration && !windows

// Purpose: the helper rendezvous listener's peer-pid checks over a real unix
//
//	socket: only the verified child pid is handed back, and a closed or
//	uninitialised connection fails the pid read. Integration-tagged because
//	the unit lane may not import net.
//
// SPORT: internal/elevation helper edge tests/ADDED (P1-SHIP-15).
package elevation

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestAcceptFromPIDOnlyHandsBackTheNamedProcess(t *testing.T) {
	TrustAnyVolumeForTest(t)
	ln, closeListener, err := listenPrivate()
	if err != nil {
		t.Fatal(err)
	}
	defer closeListener()
	if _, err := acceptFromPID(ln, time.Now().Add(-time.Second), os.Getpid()); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired deadline: err=%v", err)
	}
	other, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := acceptFromPID(ln, time.Now().Add(300*time.Millisecond), os.Getpid()+1); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a connection from another pid was accepted: err=%v", err)
	}
	mine, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mine.Close() }()
	conn, err := acceptFromPID(ln, time.Now().Add(2*time.Second), os.Getpid())
	if err != nil {
		t.Fatalf("connection from the named pid refused: %v", err)
	}
	_ = conn.Close()
}

func TestPeerPIDReportsThePeerAndFailsOnAnUnusableConnection(t *testing.T) {
	TrustAnyVolumeForTest(t)
	ln, closeListener, err := listenPrivate()
	if err != nil {
		t.Fatal(err)
	}
	defer closeListener()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := peerPID(server); err != nil || pid != os.Getpid() {
		t.Fatalf("peerPID = %d, %v; want %d", pid, err, os.Getpid())
	}
	_ = server.Close()
	if _, err := peerPID(server); err == nil {
		t.Fatal("peer pid read from a closed connection")
	}
	if _, err := peerPID(&net.UnixConn{}); err == nil {
		t.Fatal("peer pid read from an uninitialised connection")
	}
}
