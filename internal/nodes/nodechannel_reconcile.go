// Purpose: keep the controller's node tunnels in step with live record
//   state. Every ReconcileInterval tick, and on each change notification,
//   a revoked or removed node's tunnel is stopped, a drained node's tunnel
//   is stopped once no dispatch is in flight on it, and a node enrolled
//   with a complete route gets one started.
// Constraints: time comes only from the injected Ticker; a tunnel this
//   channel starts records its session so Call rides the same
//   authenticated client and nothing else.

package nodes

import (
	"context"
	"path"
	"time"
)

// ReconcileInterval is how often Reconcile re-reads the device records.
const ReconcileInterval = 10 * time.Second

// ReconcileDeps is what Reconcile needs to start and pace tunnels.
type ReconcileDeps struct {
	// Configure builds the tunnel config for a routed node: dialer, known
	// hosts and the node-facing far end. Its Target and RemoteSocketPath
	// are overwritten with the resolved target and the route's
	// controller.sock. Nil starts nothing.
	Configure func(target Target, route Route) TunnelConfig
	// NewTicker paces passes; nil means NewSystemTicker.
	NewTicker func(time.Duration) Ticker
	// Changes carries record-store change notifications; nil means ticks only.
	Changes <-chan struct{}
	// Rendezvous holds the dispatches published on nodes. A drained node
	// with one still awaiting its report keeps its tunnel, so heartbeats
	// and that report still arrive; nil means nothing is in flight.
	Rendezvous *Rendezvous
}

// SetReconcileDeps installs d before Reconcile runs.
func (c *NodeChannel) SetReconcileDeps(d ReconcileDeps) {
	c.mu.Lock()
	c.deps = d
	c.mu.Unlock()
}

// Reconcile runs one pass now, then one per tick and per change
// notification, until ctx is done. Tunnels it starts live under ctx.
func (c *NodeChannel) Reconcile(ctx context.Context) {
	c.mu.Lock()
	d := c.deps
	c.mu.Unlock()
	newTicker := d.NewTicker
	if newTicker == nil {
		newTicker = NewSystemTicker
	}
	ticker := newTicker(ReconcileInterval)
	defer ticker.Stop()
	for {
		c.reconcileOnce(ctx, d)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
		case <-d.Changes:
		}
	}
}

// reconcileOnce stops every registered tunnel whose node is no longer
// kept and starts the wanted ones that are not registered. A failed record
// read changes nothing: the next pass retries.
func (c *NodeChannel) reconcileOnce(ctx context.Context, d ReconcileDeps) {
	recs, err := c.records.List()
	if err != nil {
		return
	}
	wanted := make(map[string]bool, len(recs))
	keep := make(map[string]bool, len(recs))
	for _, rec := range recs {
		wanted[rec.NodeID] = channelWanted(rec)
		keep[rec.NodeID] = wanted[rec.NodeID] || drainingInFlight(rec, d.Rendezvous)
	}
	for _, nodeID := range c.registered() {
		if !keep[nodeID] {
			c.m.Stop(nodeID)
			c.dropSession(nodeID, nil)
		}
	}
	if d.Configure == nil || ctx.Err() != nil {
		return
	}
	for nodeID, want := range wanted {
		if _, running := c.m.State(nodeID); !want || running {
			continue
		}
		target, route, err := c.records.Target(nodeID)
		if err != nil {
			continue
		}
		cfg := d.Configure(target, route)
		cfg.Target = target
		cfg.RemoteSocketPath = path.Join(route.SocketDir, ControllerSocketName)
		cfg.Dialer = trackingDialer{inner: cfg.Dialer, c: c, nodeID: nodeID}
		c.m.Start(ctx, cfg)
	}
}

// channelWanted: an enrolled node, not revoked, not drained, with a
// complete route.
func channelWanted(rec DeviceRecord) bool {
	return !SignatureRevoked(rec, rec.PubKeyB64) && !rec.Drained && rec.NodeRoute.complete()
}

// drainingInFlight: a drained, unrevoked node with a dispatch published on
// it that has not reported yet. Its tunnel stays until that report lands.
func drainingInFlight(rec DeviceRecord, rv *Rendezvous) bool {
	if rv == nil || !rec.Drained || SignatureRevoked(rec, rec.PubKeyB64) {
		return false
	}
	rv.mu.Lock()
	defer rv.mu.Unlock()
	for _, p := range rv.pending {
		if p.attempt.NodeID == rec.NodeID {
			return true
		}
	}
	return false
}

// registered lists the node ids with a tunnel in the Manager.
func (c *NodeChannel) registered() []string {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	ids := make([]string, 0, len(c.m.tunnels))
	for id := range c.m.tunnels {
		ids = append(ids, id)
	}
	return ids
}

// dropSession forgets nodeID's session; only s when s is non-nil, so a
// stale close never drops a newer session.
func (c *NodeChannel) dropSession(nodeID string, s Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.sessions[nodeID]; ok && (s == nil || cur == s) {
		delete(c.sessions, nodeID)
	}
}

// trackingDialer records each session its inner dialer establishes as the
// node's live session, and forgets it when the tunnel closes it.
type trackingDialer struct {
	inner  Dialer
	c      *NodeChannel
	nodeID string
}

func (d trackingDialer) Dial(ctx context.Context, target Target, verify HostKeyVerifier) (Session, error) {
	s, err := d.inner.Dial(ctx, target, verify)
	if err != nil {
		return nil, err
	}
	tracked := &trackedSession{Session: s, c: d.c, nodeID: d.nodeID}
	d.c.mu.Lock()
	d.c.sessions[d.nodeID] = tracked
	d.c.mu.Unlock()
	return tracked, nil
}

type trackedSession struct {
	Session
	c      *NodeChannel
	nodeID string
}

func (s *trackedSession) Close() error {
	s.c.dropSession(s.nodeID, s)
	return s.Session.Close()
}
