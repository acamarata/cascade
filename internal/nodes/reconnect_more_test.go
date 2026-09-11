package nodes

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNextBackoff_CapWhenInitialExceedsMax: a policy whose InitialBackoff
// already exceeds MaxBackoff must cap on the very first attempt, not just
// after doubling (the loop body never runs for n=1, so the cap check
// after the loop is the only thing that can catch this).
func TestNextBackoff_CapWhenInitialExceedsMax(t *testing.T) {
	p := ReconnectPolicy{InitialBackoff: 10 * time.Second, MaxBackoff: time.Second}
	if got := nextBackoff(1, p); got != time.Second {
		t.Fatalf("nextBackoff(1) = %v, want %v (capped)", got, time.Second)
	}
}

// TestRealSleeper_FiresOnTimer proves the PRODUCTION Sleeper (not
// reconnect_test.go's fakeSleeper) actually waits and reports true when
// its timer fires before ctx ends.
func TestRealSleeper_FiresOnTimer(t *testing.T) {
	if !(realSleeper{}).Sleep(context.Background(), time.Millisecond) {
		t.Fatal("expected Sleep to report true when its timer fires")
	}
}

// TestRealSleeper_CanceledContext proves the same production Sleeper
// reports false when ctx ends before the timer fires.
func TestRealSleeper_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if (realSleeper{}).Sleep(ctx, time.Hour) {
		t.Fatal("expected Sleep to report false for an already-canceled context")
	}
}

// TestTunnelState_StringUnknown: an out-of-range TunnelState value must
// render as "unknown" (the fail-closed default case), never panic or
// render an empty string.
func TestTunnelState_StringUnknown(t *testing.T) {
	if got := TunnelState(99).String(); got != "unknown" {
		t.Fatalf("String() = %q, want %q", got, "unknown")
	}
}

// TestTunnelMaxAttemptsExhausted: a bounded ReconnectPolicy.MaxAttempts
// stops retrying and goes terminal-down once the cap is reached, rather
// than retrying forever.
func TestTunnelMaxAttemptsExhausted(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	refused := func() (Session, error) { return nil, cascade.New(cascade.KindUnavailable, "refused") }
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){refused}}
	policy := ReconnectPolicy{InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxAttempts: 2}
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: &fakeSleeper{}, Policy: policy})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tun.run(ctx)
	assertDown(t, tun)
	assertCallCount(t, dialer, 2) // stopped exactly at the cap, not before or after
}

// sessionListenErr is a Session whose ListenUnix always fails: dial
// succeeded but the remote forward itself did not, so the run loop must
// treat this as transient and reconnect rather than going terminal.
type sessionListenErr struct {
	done chan struct{}
	once sync.Once
}

func newSessionListenErr() *sessionListenErr { return &sessionListenErr{done: make(chan struct{})} }
func (s *sessionListenErr) ListenUnix(string) (Listener, error) {
	return nil, errors.New("remote forward failed")
}
func (s *sessionListenErr) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *sessionListenErr) Done() <-chan struct{} { return s.done }

// TestTunnelRunSession_ListenUnixErrorReconnects proves a ListenUnix
// failure (dial succeeded, remote forward did not) does not go terminal:
// the loop reconnects and dials again.
func TestTunnelRunSession_ListenUnixErrorReconnects(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){
		func() (Session, error) { return newSessionListenErr(), nil },
	}}
	sleeper := &fakeSleeper{}
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: sleeper})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { waitCallCount(t, dialer, 3, 2*time.Second); cancel() }()
	tun.run(ctx)
	if dialer.callCount() < 3 {
		t.Fatalf("dial called %d times, want >= 3 (kept reconnecting past the forward failure)", dialer.callCount())
	}
}

// listenerSeq is a Listener whose Accept replays a scripted sequence of
// connections before returning a terminal error — used to drive
// acceptLoop directly and deterministically, without a blocking fake.
type listenerSeq struct {
	conns []Conn
	i     int
}

func (l *listenerSeq) Accept() (Conn, error) {
	if l.i < len(l.conns) {
		c := l.conns[l.i]
		l.i++
		return c, nil
	}
	return nil, errors.New("listener exhausted")
}
func (l *listenerSeq) Close() error { return nil }

// TestAcceptLoop_LocalDialFailureThenSuccess drives acceptLoop directly
// (bypassing the blocking fakeSession machinery reconnect_test.go uses
// for the state-machine tests) over BOTH of its per-connection branches:
// a LocalDial failure that closes the remote and continues, and a
// LocalDial success that hands the pair to pipeConn.
func TestAcceptLoop_LocalDialFailureThenSuccess(t *testing.T) {
	remote1 := &memConn{r: bytes.NewReader(nil)}
	remote2 := &memConn{r: bytes.NewReader(nil)}
	local2 := &memConn{r: bytes.NewReader(nil)}
	dialCalls := 0
	tun := newTunnel(TunnelConfig{
		LocalDial: func(context.Context) (Conn, error) {
			dialCalls++
			if dialCalls == 1 {
				return nil, errors.New("local dial refused")
			}
			return local2, nil
		},
	})
	listener := &listenerSeq{conns: []Conn{remote1, remote2}}
	tun.acceptLoop(context.Background(), listener)
	if dialCalls != 2 {
		t.Fatalf("LocalDial called %d times, want 2", dialCalls)
	}
	// Give the pipeConn goroutine a moment to finish copying (both
	// readers are empty, so io.Copy returns immediately on EOF).
	deadline := time.Now().Add(time.Second)
	for local2.String() != "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// TestManagerState_TrueWhileRunning proves State reports ok=true for a
// node with an active tunnel, not just ok=false for an absent one
// (reconnect_test.go's TestManagerStart_Idempotent only checks the
// post-Stop absence case).
func TestManagerState_TrueWhileRunning(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	up := func() (Session, error) { return newFakeSession(), nil }
	dialer, m := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){up}}, NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx, TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh})
	waitCallCount(t, dialer, 1, time.Second)
	state, ok := m.State(target.NodeID)
	if !ok {
		t.Fatal("expected State to report ok=true for a running tunnel")
	}
	_ = state
	m.Stop(target.NodeID)
}
