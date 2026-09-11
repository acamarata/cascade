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

// TestNextBackoff_Table: the capped-doubling schedule, pure/deterministic.
func TestNextBackoff_Table(t *testing.T) {
	p := ReconnectPolicy{InitialBackoff: time.Second, MaxBackoff: 8 * time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := nextBackoff(i+1, p); got != w {
			t.Fatalf("nextBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

type fakeSleeper struct { // records every requested duration, never really waiting
	mu    sync.Mutex
	waits []time.Duration
}

func (f *fakeSleeper) Sleep(ctx context.Context, d time.Duration) bool {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	return ctx.Err() == nil
}
func (f *fakeSleeper) snapshot() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.waits))
	copy(out, f.waits)
	return out
}

type fakeSession struct { // its own Listener too (ListenUnix returns self); Accept blocks until Close
	ch     chan Conn
	closed chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newFakeSession() *fakeSession {
	return &fakeSession{ch: make(chan Conn), closed: make(chan struct{}), done: make(chan struct{})}
}
func (s *fakeSession) ListenUnix(string) (Listener, error) { return s, nil }
func (s *fakeSession) Accept() (Conn, error) {
	select {
	case c := <-s.ch:
		return c, nil
	case <-s.closed:
		return nil, errors.New("listener closed")
	}
}
func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.closed); close(s.done) })
	return nil
}
func (s *fakeSession) Done() <-chan struct{} { return s.done }

type fakeDialer struct { // dials a scripted outcome sequence, invoking verify each time
	mu       sync.Mutex
	calls    int
	fp       string
	outcomes []func() (Session, error)
}

func (d *fakeDialer) Dial(_ context.Context, _ Target, verify HostKeyVerifier) (Session, error) {
	d.mu.Lock()
	i := d.calls
	d.calls++
	d.mu.Unlock()
	if err := verify(d.fp); err != nil {
		return nil, err
	}
	if i >= len(d.outcomes) {
		i = len(d.outcomes) - 1
	}
	return d.outcomes[i]()
}
func (d *fakeDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
func testTarget() Target { return Target{NodeID: "n1", User: "worker", Addr: "host1:22"} }

func pinTarget(t *testing.T, kh *KnownHosts, target Target, fp string) {
	t.Helper()
	if err := kh.Pin(target.knownHostsKey(), fp, false); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
}

func waitState(t *testing.T, tun *Tunnel, want TunnelState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for tun.State() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tun.State() != want {
		t.Fatalf("state = %v, want %v within %v", tun.State(), want, timeout)
	}
}

func assertDown(t *testing.T, tun *Tunnel) {
	t.Helper()
	if got := tun.State(); got != TunnelDown {
		t.Fatalf("state = %v, want TunnelDown", got)
	}
}

func waitCallCount(t *testing.T, d *fakeDialer, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for d.callCount() < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if d.callCount() < n {
		t.Fatalf("dial called %d times within %v, want >= %d", d.callCount(), timeout, n)
	}
}

func assertCallCount(t *testing.T, d *fakeDialer, want int) {
	t.Helper()
	if got := d.callCount(); got != want {
		t.Fatalf("dial called %d times, want exactly %d", got, want)
	}
}

func TestTunnelHostKeyChangedRefused(t *testing.T) { // a changed host key is terminal
	kh := NewKnownHosts(newMemKnownHostsBackend())
	pinTarget(t, kh, testTarget(), "original-fp")
	unreachable := []func() (Session, error){func() (Session, error) { return nil, nil }} // verify refuses first
	dialer, sleeper := &fakeDialer{fp: "changed-fp", outcomes: unreachable}, &fakeSleeper{}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: kh, Sleeper: sleeper})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tun.run(ctx)
	assertDown(t, tun)
	assertCallCount(t, dialer, 1) // no reconnect on host-key change
	if len(sleeper.snapshot()) != 0 {
		t.Fatalf("backoff was waited on; host-key refusal must never retry")
	}
}

// TestTunnelUnknownHostKeyRefused: first-contact with no pin/override refuses too.
func TestTunnelUnknownHostKeyRefused(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	dialer := &fakeDialer{fp: "never-pinned-fp", outcomes: []func() (Session, error){func() (Session, error) { return nil, nil }}}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: kh})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tun.run(ctx)
	assertDown(t, tun)
	assertCallCount(t, dialer, 1)
}

// TestTunnelHostKeyVerifiedEveryAttempt: Nth reconnect verifies as the 1st did.
func TestTunnelHostKeyVerifiedEveryAttempt(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "good-fp")
	session := newFakeSession()
	dialer := &fakeDialer{fp: "good-fp", outcomes: []func() (Session, error){
		func() (Session, error) { return nil, cascade.New(cascade.KindUnavailable, "refused") },
		func() (Session, error) { return nil, cascade.New(cascade.KindUnavailable, "refused") },
		func() (Session, error) { return session, nil },
	}}
	sleeper := &fakeSleeper{} // 3 dial calls below == 3 identical host-key checks
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: sleeper})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel() // before Close: guarantees run()'s next ctx.Err() check sees it
		_ = session.Close()
	}()
	tun.run(ctx)
	assertCallCount(t, dialer, 3) // 2 failures + 1 success, same host-key check each time
	if len(sleeper.snapshot()) != 2 {
		t.Fatalf("backoff waited %d times, want 2", len(sleeper.snapshot()))
	}
}

// TestTunnelReconnectStorm_BoundedBackoff: repeated drops cap the backoff.
func TestTunnelReconnectStorm_BoundedBackoff(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	refused := func() (Session, error) { return nil, cascade.New(cascade.KindUnavailable, "refused") }
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){refused}}
	sleeper, policy := &fakeSleeper{}, ReconnectPolicy{InitialBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond}
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: sleeper, Policy: policy})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { waitCallCount(t, dialer, 6, 2*time.Second); cancel() }()
	tun.run(ctx)
	waits := sleeper.snapshot()
	if len(waits) == 0 {
		t.Fatal("expected backoff waits")
	}
	for _, w := range waits {
		if w > policy.MaxBackoff {
			t.Fatalf("backoff %v exceeded cap %v", w, policy.MaxBackoff)
		}
	}
	assertDown(t, tun)
}

func TestTunnelDropMidStreamThenShutdown(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	first := newFakeSession()
	second := newFakeSession()
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){
		func() (Session, error) { return first, nil },
		func() (Session, error) { return second, nil },
	}}
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: &fakeSleeper{}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tun.run(ctx); close(done) }()
	waitState(t, tun, TunnelUp, time.Second)
	_ = first.Close() // mid-stream drop
	waitCallCount(t, dialer, 2, time.Second)
	waitState(t, tun, TunnelUp, time.Second) // reconnected onto `second`
	cancel()                                 // shutdown while connected
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after ctx cancel (shutdown while connected)")
	}
	assertDown(t, tun)
}
func TestManagerStart_Idempotent(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	target := testTarget()
	pinTarget(t, kh, target, "fp")
	up := func() (Session, error) { return newFakeSession(), nil }
	dialer, m := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){up}}, NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t1 := m.Start(ctx, TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh})
	t2 := m.Start(ctx, TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh})
	if t1 != t2 {
		t.Fatal("Start for an already-running node id returned a different *Tunnel (duplicate)")
	}
	waitCallCount(t, dialer, 1, time.Second)
	m.Stop(target.NodeID)
	if _, ok := m.State(target.NodeID); ok {
		t.Fatal("State still reports a tunnel after Stop")
	}
}
func TestRefuseTunnelServiceOnGOOS(t *testing.T) {
	err := RefuseTunnelServiceOnGOOS("windows")
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnsupported {
		t.Fatalf("windows: expected KindUnsupported, got %v (ok=%v)", k, ok)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if err := RefuseTunnelServiceOnGOOS(goos); err != nil {
			t.Fatalf("%s: unexpected refusal: %v", goos, err)
		}
	}
}

type memConn struct { // finite reader + mutex-guarded writer (Write races the other pipeConn goroutine)
	r  *bytes.Reader
	mu sync.Mutex
	w  bytes.Buffer
}

func (c *memConn) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *memConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Write(p)
}
func (c *memConn) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.w.String() }
func (c *memConn) Close() error   { return nil }

func TestPipeConn(t *testing.T) {
	remote := &memConn{r: bytes.NewReader([]byte("hi"))}
	local := &memConn{r: bytes.NewReader([]byte("yo"))}
	pipeConn(remote, local)
	deadline := time.Now().Add(time.Second)
	for (local.String() != "hi" || remote.String() != "yo") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if local.String() != "hi" || remote.String() != "yo" {
		t.Fatalf("pipeConn: local=%q remote=%q", local.String(), remote.String())
	}
}
