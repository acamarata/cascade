package rpc

// Purpose: the owner-peer seam (owner_peer.go): the guard installs once, the
// peer pid reads back from the connection context, and the start time names one
// process. The guard's judgement of requests is owner_peer_guard_test.go.
// Constraints: no "net" or "net/http" import (the no-network unit lane, Art.7.2):
//   the peer credential and pid are injected through the same context keys
//   ConnContext fills; the real socket path is proven by the agents package
//   integration tests. Refusal bodies are asserted by identity and text.

import (
	"context"
	"errors"
	"os"
	goruntime "runtime"
	"testing"
)

var _ = DeclareAccess("test.ownerpeer.owner", AccessOwnerOnly)
var _ = DeclareAccess("test.ownerpeer.scoped", AccessScoped)

// resetOwnerPeerGuard clears the process-wide guard now and at cleanup, so one
// test's installation never leaks into another's.
func resetOwnerPeerGuard(t *testing.T) {
	t.Helper()
	ownerPeerGuard.Store(nil)
	t.Cleanup(func() { ownerPeerGuard.Store(nil) })
}

// useStartTime makes every pid read as having started at start (0 = unreadable)
// for the test, restoring the OS reader at cleanup.
func useStartTime(t *testing.T, start uint64) {
	t.Helper()
	prev := peerStartTime
	peerStartTime = func(int) (uint64, bool) { return start, start != 0 }
	t.Cleanup(func() { peerStartTime = prev })
}

// peerRecorder is a guard that records every pid it is asked about.
type peerRecorder struct {
	pids []int
	err  error
}

func (p *peerRecorder) guard(_ context.Context, pid int) error {
	p.pids = append(p.pids, pid)
	return p.err
}

func TestOwnerPeerGuardInstalledOnce(t *testing.T) {
	resetOwnerPeerGuard(t)
	mustPanic := func(name, want string, f func()) {
		t.Helper()
		defer func() {
			if got, _ := recover().(string); got != want {
				t.Errorf("%s: panic %q, want %q", name, got, want)
			}
		}()
		f()
	}
	mustPanic("nil guard", "rpc: SetOwnerPeerGuard: nil guard", func() { SetOwnerPeerGuard(nil) })
	if ownerPeerGuard.Load() != nil {
		t.Fatal("a nil guard was stored")
	}
	rec := &peerRecorder{}
	SetOwnerPeerGuard(rec.guard)
	if ownerPeerGuard.Load() == nil {
		t.Fatal("the first install did not store the guard")
	}
	other := &peerRecorder{err: errors.New("other")}
	mustPanic("second install", "rpc: SetOwnerPeerGuard: a guard is already installed", func() { SetOwnerPeerGuard(other.guard) })
	_ = (*ownerPeerGuard.Load())(context.Background(), 7)
	if len(rec.pids) != 1 || rec.pids[0] != 7 || len(other.pids) != 0 {
		t.Errorf("the first guard was replaced: first %v, second %v", rec.pids, other.pids)
	}
}

func TestPeerPIDFromConnection(t *testing.T) {
	if pid, ok := PeerPIDFrom(context.Background()); ok || pid != 0 {
		t.Errorf("a bare context reports pid %d ok %v, want unknown", pid, ok)
	}
	for name, tc := range map[string]struct {
		pid int
		ok  bool
	}{
		"unresolved":       {4242, false},
		"zero pid":         {0, true},
		"negative pid":     {-3, true},
		"unresolved, zero": {0, false},
	} {
		if pid, ok := PeerPIDFrom(ctxWithPeerPID(context.Background(), tc.pid, 7, tc.ok)); ok || pid != 0 {
			t.Errorf("%s: pid %d ok %v, want unknown", name, pid, ok)
		}
	}
	ctx := ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), 4242, 7, true)
	if pid, ok := PeerPIDFrom(ctx); !ok || pid != 4242 {
		t.Errorf("a resolved pid reads back as %d ok %v, want 4242", pid, ok)
	}
	if cred, _ := ctx.Value(peerCredKey{}).(peerCred); !cred.ok {
		t.Error("storing the pid dropped the peer credential")
	}
}

func TestPeerStartTimeNamesOneProcess(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("no peer pid on windows")
	}
	first, ok := osPeerStartTime(os.Getpid())
	again, ok2 := osPeerStartTime(os.Getpid())
	if !ok || !ok2 || first == 0 || first != again {
		t.Fatalf("start time of this process: %d,%v then %d,%v, want one nonzero stable value", first, ok, again, ok2)
	}
	if v, ok := osPeerStartTime(1 << 30); ok || v != 0 {
		t.Errorf("a pid that cannot exist reads as start %d ok %v, want none", v, ok)
	}
}

func TestGuardedConnContextArmsTheConnection(t *testing.T) {
	func() {
		defer func() {
			if r := recover(); r != "rpc: GuardedConnContext: nil next" {
				t.Errorf("nil next: recovered %v, want the nil-next panic", r)
			}
		}()
		GuardedConnContext(nil)
	}()
	ctx := GuardedConnContext(ConnContext)(context.Background(), nil)
	_, ran := ctx.Value(peerCredKey{}).(peerCred)
	if armed, _ := ctx.Value(peerArmedKey{}).(bool); !ran || !armed {
		t.Errorf("next ran %v, armed %v; want both", ran, armed)
	}
	if _, ok := PeerPIDFrom(ctx); ok {
		t.Error("a connection with no peer pid reads as resolved")
	}
}
