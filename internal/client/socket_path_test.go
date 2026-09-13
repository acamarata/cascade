package client

// Purpose: DEFECT-recall-no-embedded-path.md's secondary finding —
//
//	UnixDialer never checked whether socketPath fits in sockaddr_un's
//	fixed-size sun_path field, so a HOME deep enough to push
//	<HOME>/.cascade/daemon.sock past the platform's limit failed with a
//	bare "connect: invalid argument" instead of a named cause. Proves
//	unixSocketPathError's boundary on both sides, and that UnixDialer
//	itself refuses BEFORE any connect(2) — all without dialing, so this
//	file needs no "net" import (the default no-network unit lane,
//	mirroring transport_test.go's own house style).
//
// SPORT: internal/client (FIX, socket path length taxonomy error).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestUnixSocketPathError_WithinLimitIsNil proves an ordinary short path
// (the common case: a socket under a normal HOME) is never refused.
func TestUnixSocketPathError_WithinLimitIsNil(t *testing.T) {
	if err := unixSocketPathError("/tmp/short.sock"); err != nil {
		t.Fatalf("unixSocketPathError(short path) = %v, want nil", err)
	}
}

// TestUnixSocketPathError_AtLimitRefuses proves the boundary is inclusive:
// a path exactly at this platform's byte capacity leaves no room for
// sun_path's own trailing NUL and must be refused, not merely one byte
// past it.
func TestUnixSocketPathError_AtLimitRefuses(t *testing.T) {
	limit := maxUnixSocketPathBytes()
	path := strings.Repeat("a", limit)
	err := unixSocketPathError(path)
	if err == nil {
		t.Fatalf("unixSocketPathError(%d bytes) = nil, want a refusal at the %d-byte limit", limit, limit)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want it to name the socket path", err)
	}
}

// TestUnixSocketPathError_OneByteUnderLimitIsNil proves the other side of
// the same boundary: the largest path that DOES leave room for the
// trailing NUL is accepted.
func TestUnixSocketPathError_OneByteUnderLimitIsNil(t *testing.T) {
	limit := maxUnixSocketPathBytes()
	path := strings.Repeat("a", limit-1)
	if err := unixSocketPathError(path); err != nil {
		t.Fatalf("unixSocketPathError(%d bytes) = %v, want nil (one byte under the %d-byte limit)",
			limit-1, err, limit)
	}
}

// TestUnixDialer_RefusesBeforeDialing proves the check runs INSIDE
// UnixDialer itself, not only in the helper, and that it fires before any
// connect(2): a path shaped like T0's real macOS reproduction (126 bytes
// against darwin's 104-byte limit) never reaches the kernel, so this needs
// no listening peer.
func TestUnixDialer_RefusesBeforeDialing(t *testing.T) {
	limit := maxUnixSocketPathBytes()
	path := strings.Repeat("x", limit+22)
	_, err := UnixDialer(context.Background(), path)
	if err == nil {
		t.Fatal("UnixDialer(over-limit path) = nil error, want the socket path length refusal")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if strings.Contains(err.Error(), "invalid argument") {
		t.Errorf("err = %v, still the bare syscall message this fix replaces", err)
	}
}
