package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

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
	return slices.Clone(f.waits)
}

type fakeSession struct { // its own Listener too (ListenUnix returns self); Accept blocks until Close
	closed, done chan struct{}
	once         sync.Once
}

func newFakeSession() *fakeSession {
	return &fakeSession{closed: make(chan struct{}), done: make(chan struct{})}
}
func (s *fakeSession) ListenUnix(string) (Listener, error) { return s, nil }
func (s *fakeSession) DialUnix(string) (Conn, error)       { return nil, errors.New("fake: no node socket") }
func (s *fakeSession) Accept() (Conn, error)               { <-s.closed; return nil, errors.New("listener closed") }
func (s *fakeSession) Done() <-chan struct{}               { return s.done }
func (s *fakeSession) Close() error                        { s.once.Do(func() { close(s.closed); close(s.done) }); return nil }
func (s *sessionListenErr) DialUnix(string) (Conn, error) {
	return nil, errors.New("fake: no node socket")
} // reconnect_more_test.go's fake

type fakeDialer struct { // dials a scripted outcome sequence, invoking verify each time
	mu       sync.Mutex
	calls    int
	fp       string
	fps      []string // per-call presented key; fp past its end
	lastErr  error
	outcomes []func() (Session, error)
}

func (d *fakeDialer) Dial(_ context.Context, _ Target, verify HostKeyVerifier) (Session, error) {
	d.mu.Lock()
	i, fp := d.calls, d.fp
	if i < len(d.fps) {
		fp = d.fps[i]
	}
	d.calls++
	err := verify(fp) // never re-enters the dialer
	d.lastErr = err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d.outcomes[min(i, len(d.outcomes)-1)]()
}
func (d *fakeDialer) callCount() int { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }
func testTarget() Target             { return Target{NodeID: "n1", User: "worker", Addr: "host1:22"} }

func pinTarget(t *testing.T, kh *KnownHosts, target Target, fp string) {
	t.Helper()
	if err := kh.Pin(target.knownHostsKey(), fp, false); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
}
func pinnedKH(t *testing.T, fp string) *KnownHosts { // testTarget pinned to fp
	t.Helper()
	kh := NewKnownHosts(newMemKnownHostsBackend())
	pinTarget(t, kh, testTarget(), fp)
	return kh
}
func eventually(timeout time.Duration, ok func() bool) bool {
	for deadline := time.Now().Add(timeout); !ok() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	return ok()
}
func waitState(t *testing.T, tun *Tunnel, want TunnelState, timeout time.Duration) {
	t.Helper()
	if !eventually(timeout, func() bool { return tun.State() == want }) {
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
	if !eventually(timeout, func() bool { return d.callCount() >= n }) {
		t.Fatalf("dial called %d times within %v, want >= %d", d.callCount(), timeout, n)
	}
}
func assertCallCount(t *testing.T, d *fakeDialer, want int) {
	t.Helper()
	if got := d.callCount(); got != want {
		t.Fatalf("dial called %d times, want exactly %d", got, want)
	}
}

// assertRefusedOnce: one dial, terminal down, no backoff (R-21.220).
func assertRefusedOnce(t *testing.T, kh *KnownHosts, fp string) {
	t.Helper()
	dialer, sleeper := &fakeDialer{fp: fp, outcomes: []func() (Session, error){func() (Session, error) { return nil, nil }}}, &fakeSleeper{}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: kh, Sleeper: sleeper})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tun.run(ctx)
	assertDown(t, tun)
	assertCallCount(t, dialer, 1)
	if len(sleeper.snapshot()) != 0 {
		t.Fatalf("backoff was waited on; host-key refusal must never retry")
	}
}

// TestTunnelHostKeyChangedRefused: a changed host key is terminal.
func TestTunnelHostKeyChangedRefused(t *testing.T) {
	assertRefusedOnce(t, pinnedKH(t, "original-fp"), "changed-fp")
}

// TestTunnelUnknownHostKeyRefused: first-contact with no pin/override refuses too.
func TestTunnelUnknownHostKeyRefused(t *testing.T) {
	assertRefusedOnce(t, NewKnownHosts(newMemKnownHostsBackend()), "never-pinned-fp")
}

// TestChannelRefusesChangedHostKey: a started tunnel whose host later
// presents a changed key goes terminal: down, no retry, no call delivered.
func TestChannelRefusesChangedHostKey(t *testing.T) {
	kh, target, first, sleeper := pinnedKH(t, "pinned-fp"), testTarget(), newFakeSession(), &fakeSleeper{}
	dialer := &fakeDialer{fps: []string{"pinned-fp", "changed-fp"}, outcomes: []func() (Session, error){
		func() (Session, error) { return first, nil }, func() (Session, error) { t.Error("changed key reached a session"); return nil, nil }}}
	var delivered atomic.Int32
	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: sleeper,
		LocalDial: func(context.Context) (Conn, error) { delivered.Add(1); return nil, errors.New("unused") }})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { eventually(time.Second, func() bool { return tun.State() == TunnelUp }); _ = first.Close() }() // drop; redial sees a new key
	tun.run(ctx)
	assertDown(t, tun)
	assertCallCount(t, dialer, 2)
	want := ErrHostKeyChanged(target.knownHostsKey())
	if k, _ := cascade.KindOf(dialer.lastErr); k != cascade.KindPermissionDenied || dialer.lastErr.Error() != want.Error() {
		t.Fatalf("refusal = %v, want %v", dialer.lastErr, want)
	}
	if n := len(sleeper.snapshot()); n != 0 || delivered.Load() != 0 || ctx.Err() != nil {
		t.Fatalf("backoff waits %d, calls delivered %d, ctx %v; want a prompt terminal stop", n, delivered.Load(), ctx.Err())
	}
}

type testEnvelopeSigner struct{ priv ed25519.PrivateKey }

func (s testEnvelopeSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }
func (s testEnvelopeSigner) Sign(_ context.Context, m []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, m), nil
}

// TestSSHDialerFromSignerUsesInstallationKey: the userauth key is the
// Signer's Ed25519 public key, and its signatures verify under it.
func TestSSHDialerFromSignerUsesInstallationKey(t *testing.T) {
	s := testEnvelopeSigner{ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))}
	signer, err := NewSSHDialerFromSigner(s, "controller", 0).(*sshDialer).authSigner(context.Background())
	want, _ := ssh.NewPublicKey(s.PublicKey())
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || !bytes.Equal(signer.PublicKey().Marshal(), want.Marshal()) {
		t.Fatalf("userauth key (err %v) is not the installation key %x", err, want.Marshal())
	}
	sig, err := signer.Sign(nil, []byte("session-id"))
	if err != nil || signer.PublicKey().Verify([]byte("session-id"), sig) != nil {
		t.Fatalf("signature does not verify under the installation key: %v", err)
	}
	_, err = NewSSHDialerFromSigner(nil, "controller", 0).Dial(context.Background(), testTarget(), nil)
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput || err.Error() != "invalid-input: nodes: ssh dialer has no installation signer" {
		t.Fatalf("nil signer: got %v, want the KindInvalidInput refusal", err)
	}
}

var refusedDial = func() (Session, error) { return nil, cascade.New(cascade.KindUnavailable, "refused") }

// TestTunnelHostKeyVerifiedEveryAttempt: Nth reconnect verifies as the 1st did.
func TestTunnelHostKeyVerifiedEveryAttempt(t *testing.T) {
	session, sleeper := newFakeSession(), &fakeSleeper{} // 3 dial calls below == 3 identical host-key checks
	dialer := &fakeDialer{fp: "good-fp", outcomes: []func() (Session, error){
		refusedDial, refusedDial, func() (Session, error) { return session, nil }}}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: pinnedKH(t, "good-fp"), Sleeper: sleeper})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel(); _ = session.Close() }() // cancel first: run's next ctx check sees it
	tun.run(ctx)
	assertCallCount(t, dialer, 3) // 2 failures + 1 success, same host-key check each time
	if len(sleeper.snapshot()) != 2 {
		t.Fatalf("backoff waited %d times, want 2", len(sleeper.snapshot()))
	}
}

// TestTunnelReconnectStorm_BoundedBackoff: repeated drops cap the backoff.
func TestTunnelReconnectStorm_BoundedBackoff(t *testing.T) {
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){refusedDial}}
	sleeper, policy := &fakeSleeper{}, ReconnectPolicy{InitialBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: pinnedKH(t, "fp"), Sleeper: sleeper, Policy: policy})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { waitCallCount(t, dialer, 6, 2*time.Second); cancel() }()
	tun.run(ctx)
	if waits := sleeper.snapshot(); len(waits) == 0 || slices.Max(waits) > policy.MaxBackoff {
		t.Fatalf("backoff waits %v: want some, none above the cap %v", waits, policy.MaxBackoff)
	}
	assertDown(t, tun)
}
func TestTunnelDropMidStreamThenShutdown(t *testing.T) {
	first, second := newFakeSession(), newFakeSession()
	dialer := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){
		func() (Session, error) { return first, nil }, func() (Session, error) { return second, nil }}}
	tun := newTunnel(TunnelConfig{Target: testTarget(), Dialer: dialer, KnownHosts: pinnedKH(t, "fp"), Sleeper: &fakeSleeper{}})
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
	kh, target := pinnedKH(t, "fp"), testTarget()
	up := func() (Session, error) { return newFakeSession(), nil }
	dialer, m := &fakeDialer{fp: "fp", outcomes: []func() (Session, error){up}}, NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t1 := m.Start(ctx, TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh})
	if t2 := m.Start(ctx, TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh}); t1 != t2 {
		t.Fatal("Start for an already-running node id returned a different *Tunnel (duplicate)")
	}
	waitCallCount(t, dialer, 1, time.Second)
	m.Stop(target.NodeID)
	if _, ok := m.State(target.NodeID); ok {
		t.Fatal("State still reports a tunnel after Stop")
	}
}
func TestRefuseTunnelServiceOnGOOS(t *testing.T) {
	if k, ok := cascade.KindOf(RefuseTunnelServiceOnGOOS("windows")); !ok || k != cascade.KindUnsupported {
		t.Fatalf("windows: expected KindUnsupported, got %v (ok=%v)", k, ok)
	}
	if l, d := RefuseTunnelServiceOnGOOS("linux"), RefuseTunnelServiceOnGOOS("darwin"); l != nil || d != nil {
		t.Fatalf("unexpected refusal: linux %v, darwin %v", l, d)
	}
}

type memConn struct { // finite reader + mutex-guarded writer (Write races the other pipeConn goroutine)
	r  *bytes.Reader
	mu sync.Mutex
	w  bytes.Buffer
}

func (c *memConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *memConn) Write(p []byte) (int, error) { c.mu.Lock(); defer c.mu.Unlock(); return c.w.Write(p) }
func (c *memConn) String() string              { c.mu.Lock(); defer c.mu.Unlock(); return c.w.String() }
func (c *memConn) Close() error                { return nil }
func TestPipeConn(t *testing.T) {
	remote, local := &memConn{r: bytes.NewReader([]byte("hi"))}, &memConn{r: bytes.NewReader([]byte("yo"))}
	pipeConn(remote, local)
	if !eventually(time.Second, func() bool { return local.String() == "hi" && remote.String() == "yo" }) {
		t.Fatalf("pipeConn: local=%q remote=%q", local.String(), remote.String())
	}
}
