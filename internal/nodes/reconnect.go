// Purpose: the reconnect state machine Tunnel drives (down -> reconnecting
//	-> up, capped exponential backoff), the forward accept loop once a
//	session is up, and Manager, the per-node tunnel registry.
// Inputs: a TunnelConfig (Target, Dialer, KnownHosts, events, Sleeper,
//	ReconnectPolicy, the remote socket to forward, the local dial func).
// Outputs: TunnelState transitions, emitted as events and readable via
//	Tunnel.State/Manager.State; a forwarded byte stream while up.
// Constraints: R-21.220 — a host-key refusal (KindPermissionDenied) is
//	TERMINAL: sets TunnelDown, returns, never retries. Every other dial
//	failure is transient and feeds capped exponential backoff, waited on
//	Sleeper (never a bare time.Sleep — Art.7.3), bounded by ctx and,
//	optionally, ReconnectPolicy.MaxAttempts. Manager.Start is idempotent
//	per node id — never a duplicate tunnel/forward.
// SPORT: internal/nodes Tunnel/ADDED, ReconnectPolicy/ADDED, Manager/ADDED
//	(P1-E17-W4-S36-T3).

package nodes

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReconnectPolicy bounds the tunnel's reconnect backoff.
type ReconnectPolicy struct {
	// InitialBackoff is the first attempt's delay; each later attempt
	// doubles it, capped at MaxBackoff.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// MaxAttempts bounds consecutive failures before giving up. Zero
	// means no cap (still bounded via ctx + the capped backoff).
	MaxAttempts int
}

// DefaultReconnectPolicy backs a zero TunnelConfig.Policy.
var DefaultReconnectPolicy = ReconnectPolicy{InitialBackoff: time.Second, MaxBackoff: 60 * time.Second}

func (p ReconnectPolicy) resolved() ReconnectPolicy {
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = DefaultReconnectPolicy.InitialBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultReconnectPolicy.MaxBackoff
	}
	return p
}

// nextBackoff: InitialBackoff * 2^(n-1), capped at MaxBackoff (mirrors
// restart.go's backoffFor). Pure, so the schedule is table-tested.
func nextBackoff(n int, p ReconnectPolicy) time.Duration {
	p = p.resolved()
	d := p.InitialBackoff
	for i := 1; i < n; i++ {
		if d >= p.MaxBackoff {
			return p.MaxBackoff
		}
		d *= 2
	}
	if d > p.MaxBackoff {
		return p.MaxBackoff
	}
	return d
}

// Sleeper abstracts the reconnect loop's backoff wait so tests never sit
// through a real delay (Art.7.3, mirrors scheduler.go's waitOrDone).
// Sleep blocks for d or until ctx is done, returning false iff ctx ended
// the wait first.
type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) bool
}

// realSleeper is the production Sleeper.
type realSleeper struct{}

func (realSleeper) Sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// TunnelConfig configures one Tunnel/Manager.Start call.
type TunnelConfig struct {
	Target     Target
	Dialer     Dialer
	KnownHosts *KnownHosts
	// HostKeyOverride mirrors EnrollPayload.HostKeyOverride: the operator
	// --host-key-fingerprint value, "" when not supplied.
	HostKeyOverride string
	Events          TunnelEventPublisher
	Clock           Clock
	Sleeper         Sleeper
	Policy          ReconnectPolicy
	// RemoteSocketPath is the NODE-side path the tunnel remote-forwards
	// (ssh -R streamlocal, the D-24 pattern).
	RemoteSocketPath string
	// LocalDial opens the forward's far end: the controller's own local
	// RPC socket, dialed fresh per forwarded connection.
	LocalDial func(ctx context.Context) (Conn, error)
}

func (c TunnelConfig) resolved() TunnelConfig {
	if c.Events == nil {
		c.Events = DiscardTunnelEvents{}
	}
	if c.Sleeper == nil {
		c.Sleeper = realSleeper{}
	}
	c.Policy = c.Policy.resolved()
	return c
}

// Tunnel is one node's managed reconnecting ssh tunnel.
type Tunnel struct {
	cfg      TunnelConfig
	mu       sync.Mutex
	state    TunnelState
	stopOnce sync.Once
	cancel   context.CancelFunc
}

func newTunnel(cfg TunnelConfig) *Tunnel {
	return &Tunnel{cfg: cfg.resolved(), state: TunnelDown}
}

// State reports the tunnel's current TunnelState.
func (t *Tunnel) State() TunnelState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *Tunnel) setState(ctx context.Context, s TunnelState, event string) {
	t.mu.Lock()
	t.state = s
	t.mu.Unlock()
	t.cfg.Events.Publish(ctx, event, map[string]interface{}{"node_id": t.cfg.Target.NodeID, "state": s.String()})
}

// stop cancels the tunnel's run loop. Idempotent.
func (t *Tunnel) stop() {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		cancel := t.cancel
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

// run drives down -> reconnecting -> up until ctx is done or a host-key
// refusal makes it terminal. Runs in its own goroutine (Manager.Start).
func (t *Tunnel) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	defer cancel()

	attempt := 0
	verify := func(fp string) error {
		return t.cfg.KnownHosts.Verify(t.cfg.Target.knownHostsKey(), fp, t.cfg.HostKeyOverride)
	}
	for {
		if ctx.Err() != nil {
			t.setState(parent, TunnelDown, "node.tunnel.down")
			return
		}
		t.setState(ctx, TunnelReconnecting, "node.tunnel.reconnecting")
		session, err := t.cfg.Dialer.Dial(ctx, t.cfg.Target, verify)
		if err != nil {
			if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindPermissionDenied {
				// R-21.220: a host-key refusal is terminal — never retry.
				t.setState(parent, TunnelDown, "node.tunnel.host_key_refused")
				return
			}
			attempt++
			if t.cfg.Policy.MaxAttempts > 0 && attempt >= t.cfg.Policy.MaxAttempts {
				t.setState(parent, TunnelDown, "node.tunnel.reconnect_exhausted")
				return
			}
			if !t.cfg.Sleeper.Sleep(ctx, nextBackoff(attempt, t.cfg.Policy)) {
				t.setState(parent, TunnelDown, "node.tunnel.down")
				return
			}
			continue
		}
		attempt = 0
		t.setState(ctx, TunnelUp, "node.tunnel.up")
		t.runSession(ctx, session)
		_ = session.Close()
	}
}

// runSession opens the remote forward and accepts+forwards connections
// until the session ends or ctx is done.
func (t *Tunnel) runSession(ctx context.Context, session Session) {
	listener, err := session.ListenUnix(t.cfg.RemoteSocketPath)
	if err != nil {
		return // dial succeeded but the forward itself failed; loop reconnects
	}
	defer func() { _ = listener.Close() }()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		t.acceptLoop(ctx, listener)
	}()

	select {
	case <-ctx.Done():
	case <-session.Done():
	case <-acceptDone:
	}
}

// acceptLoop accepts forwarded connections and pipes each to a freshly
// dialed local connection, until Accept fails.
func (t *Tunnel) acceptLoop(ctx context.Context, listener Listener) {
	for {
		remote, err := listener.Accept()
		if err != nil {
			return
		}
		local, err := t.cfg.LocalDial(ctx)
		if err != nil {
			_ = remote.Close()
			continue
		}
		go pipeConn(remote, local)
	}
}

// pipeConn copies bytes both directions until either side closes.
func pipeConn(remote, local Conn) {
	defer func() { _ = remote.Close() }()
	defer func() { _ = local.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	<-done
}

// Manager is the per-controller registry of active tunnels, one per node
// id. Start is idempotent: never a duplicate tunnel/forward.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*Tunnel
}

// NewManager returns an empty Manager.
func NewManager() *Manager { return &Manager{tunnels: make(map[string]*Tunnel)} }

// Start begins (or returns the already-running) tunnel for cfg.Target.NodeID.
func (m *Manager) Start(ctx context.Context, cfg TunnelConfig) *Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.tunnels[cfg.Target.NodeID]; ok {
		return existing
	}
	t := newTunnel(cfg)
	m.tunnels[cfg.Target.NodeID] = t
	go t.run(ctx)
	return t
}

// Stop ends and forgets nodeID's tunnel, if any.
func (m *Manager) Stop(nodeID string) {
	m.mu.Lock()
	t, ok := m.tunnels[nodeID]
	delete(m.tunnels, nodeID)
	m.mu.Unlock()
	if ok {
		t.stop()
	}
}

// State reports nodeID's TunnelState and whether it is registered — the
// `node status <id>` surface (S-36.T4) reads this.
func (m *Manager) State(nodeID string) (TunnelState, bool) {
	m.mu.Lock()
	t, ok := m.tunnels[nodeID]
	m.mu.Unlock()
	if !ok {
		return TunnelDown, false
	}
	return t.State(), true
}
