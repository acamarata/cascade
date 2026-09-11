// Purpose (this file): Compositor, the in-memory FleetSnapshot builder.
// Merges provider-registry state, node-registry state, and the local
// resource sampler into one guarded FleetSnapshot, expiring any slot whose
// source has gone stale past its TTL - lazily, at read time, so no
// background goroutine or ticker is needed (Snapshot always reflects
// "clock.Now() vs each slot's UpdatedAt", never a cached decision made in
// the past).
//
// Inputs: registry.ProviderRecord/LaneRecord (via ProviderSource),
// nodes.DeviceRecord (via NodeSource), governor.ResourceSnapshot (via
// UpdateSampler), and per-node DIRECT-probe outcomes (via RecordProbe).
// Outputs: FleetSnapshot, read through Snapshot().
//
// Constraints: every timestamp comes from the injected Clock (Art.7.3); the
// live maps are guarded by one sync.RWMutex; Update* never trusts a second
// source's opinion about the first - each figure below is traced to the
// exact call that derives it.
//
// SOURCE-TRUST NOTE (the ticket's own warning, applied literally): a
// provider's State comes ONLY from that provider's own LaneRecord
// rows (registry, real); it is never inferred from, e.g., the node domain
// or the sampler. A node's Presence comes ONLY from that node's own
// persisted DeviceRecord.Presence field; it is never inferred from
// provider state. Each Update* method touches exactly one source's data.
//
// PRESENCE MIGRATION (P1-E36-W7-S72-T2). This Compositor previously
// computed PresenceState itself (a documented stopgap, recorded in this
// file's history) because no real classifier existed yet. P1-E36-W7-S72-T2
// landed nodes.Presence/nodes.AdvancePresence/nodes.Prober, the real
// hysteresis state machine R-21.65/R-21.197 specify, and persists its
// result directly on DeviceRecord.Presence (nodes/records.go). This file
// now reads that field verbatim (buildNodeSlot) rather than re-deriving
// it from LastSeen + a locally-owned probe-miss counter - retiring the
// stopgap so there is exactly one presence classifier in the tree, per
// R-16.79's duplicate-registry rule. RecordProbe/derivePresence/
// probeMisses are removed with it: probing is nodes.Prober's job now, not
// this compositor's.
//
// SPORT: fleet.capacity.snapshot (compositor, ADD, per T-1 sport_updates).

package capacity

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// Clock abstracts the wall clock (Art.7.3), structurally identical to
// registry.Clock/usage.Clock so any of their concrete types (or
// internal/runtime's) satisfy it with zero adapter code.
type Clock interface {
	Now() time.Time
}

// ProviderSource is the seam Compositor.UpdateProviders reads through -
// exactly *registry.Registry's own two read methods, duck-typed so tests
// can supply a deterministic double without a real *sql.DB.
type ProviderSource interface {
	ListProviders(ctx context.Context) ([]registry.ProviderRecord, error)
	ListLanes(ctx context.Context) ([]registry.LaneRecord, error)
}

// NodeSource is the seam Compositor.UpdateNodes reads through - exactly
// *nodes.RecordStore.List's own shape.
type NodeSource interface {
	List() ([]nodes.DeviceRecord, error)
}

// ChangeFunc is invoked synchronously, after the mutex is released, once
// per Update* call whose recomputed snapshot differs materially (per
// diff.go's Diff) from the previously observed one. seq is the new
// snapshot's Seq. rpc.go's SSE wiring is the intended caller of
// SetOnChange; nil (the default) means no notification is attempted.
type ChangeFunc func(snap FleetSnapshot, delta *Delta, seq uint64)

// Compositor is the guarded, in-memory FleetSnapshot builder. The zero
// value is not usable; construct with NewCompositor.
type Compositor struct {
	mu    sync.RWMutex
	clock Clock
	ttl   time.Duration

	providers  map[string]ProviderSlot
	nodes      map[string]NodeSlot
	selfNodeID string
	seq        uint64
	prev       FleetSnapshot
	havePrev   bool
	onChange   ChangeFunc
}

// NewCompositor returns a ready Compositor. ttl bounds how long a
// provider/node slot may go without a fresh Update* call before Snapshot
// reports it StateUnknown/PresenceUnknown. selfNodeID names the node
// UpdateSampler's readings apply to (the local daemon's own host); empty
// means UpdateSampler is a no-op.
//
// P1-E36-W7-S72-T2 removed the heartbeat-timeout parameter this
// constructor previously took: Presence now comes verbatim from
// DeviceRecord.Presence (nodes.Prober's own output), so this Compositor
// no longer re-derives it from a heartbeat staleness window itself.
func NewCompositor(clock Clock, ttl time.Duration, selfNodeID string) *Compositor {
	return &Compositor{
		clock:      clock,
		ttl:        ttl,
		providers:  make(map[string]ProviderSlot),
		nodes:      make(map[string]NodeSlot),
		selfNodeID: selfNodeID,
	}
}

// SetOnChange installs fn as the change notifier. Not safe to call
// concurrently with Update*/Snapshot; the composition root calls it once,
// before wiring any source.
func (c *Compositor) SetOnChange(fn ChangeFunc) { c.onChange = fn }

// UpdateProviders recomputes every ProviderSlot from src's current
// ListProviders/ListLanes result. A provider with zero lanes for a given
// BucketKind reports that bucket StateUnknown (task-spec error path:
// "provider usage endpoint error -> bucket fields unknown" generalizes to
// "no lane data for this bucket at all").
func (c *Compositor) UpdateProviders(ctx context.Context, src ProviderSource) error {
	recs, err := src.ListProviders(ctx)
	if err != nil {
		return err
	}
	lanes, err := src.ListLanes(ctx)
	if err != nil {
		return err
	}
	now := c.clock.Now()
	byProvider := make(map[string][]registry.LaneRecord)
	for _, l := range lanes {
		byProvider[l.ProviderName] = append(byProvider[l.ProviderName], l)
	}

	next := make(map[string]ProviderSlot, len(recs))
	for _, rec := range recs {
		next[rec.Name] = buildProviderSlot(rec, byProvider[rec.Name], now)
	}

	c.mu.Lock()
	c.providers = next
	c.mu.Unlock()
	c.notify()
	return nil
}

// UpdateNodes recomputes every NodeSlot from src's current List result.
// Presence is read verbatim from each record's own persisted
// DeviceRecord.Presence field (nodes.Prober/nodes.AdvancePresence's
// output) - see this file's PRESENCE MIGRATION note.
func (c *Compositor) UpdateNodes(_ context.Context, src NodeSource) error {
	recs, err := src.List()
	if err != nil {
		return err
	}
	now := c.clock.Now()

	c.mu.Lock()
	next := make(map[string]NodeSlot, len(recs))
	for _, rec := range recs {
		next[rec.NodeID] = buildNodeSlot(rec, now)
	}
	c.nodes = next
	c.mu.Unlock()
	c.notify()
	return nil
}

// UpdateSampler applies snap to the self node (selfNodeID), setting Load
// from CPUFraction. Hardware-inventory fields (cpu_cores/ram_mb/gpu/
// toolchains) are never set here: no sampler or census source in the tree
// reports them - see snapshot.go's NodeSlot doc comment and this
// Compositor's own top-level note. A zero-value selfNodeID makes this a
// no-op, matching NewCompositor's documented default.
func (c *Compositor) UpdateSampler(snap governor.ResourceSnapshot) {
	if c.selfNodeID == "" {
		return
	}
	c.mu.Lock()
	slot, exists := c.nodes[c.selfNodeID]
	if !exists {
		slot = NodeSlot{ID: c.selfNodeID, Presence: PresenceReachable}
	}
	slot.Load = snap.CPUFraction
	slot.UpdatedAt = c.clock.Now()
	slot.Diagnostic = hardwareUnavailableDiagnostic
	c.nodes[c.selfNodeID] = slot
	c.mu.Unlock()
	c.notify()
}

// hardwareUnavailableDiagnostic is the diagnostic every NodeSlot carries
// today: no source in the tree reports per-node cpu_cores/ram_mb/gpu/
// toolchains (P1-E12-W3-S24-T3's real scope is the sessions storage
// domain, not a hardware census - see this package's journal).
const hardwareUnavailableDiagnostic = "node hardware inventory unavailable: no census source reports cpu_cores/ram_mb/gpu/toolchains in this tree"

// notify recomputes the snapshot and fires onChange if it differs
// materially from the last observed one. Called with the mutex released.
func (c *Compositor) notify() {
	c.mu.Lock()
	c.notifyLocked()
	c.mu.Unlock()
}

// notifyLocked is notify's body, for callers that already hold c.mu (e.g.
// RecordProbe). Must be called with c.mu held.
func (c *Compositor) notifyLocked() {
	snap := c.buildSnapshotLocked()
	var delta *Delta
	if c.havePrev {
		delta = Diff(c.prev, snap)
	} else {
		delta = Diff(FleetSnapshot{}, snap)
	}
	if delta == nil {
		return
	}
	c.seq++
	snap.Seq = c.seq
	c.prev = Clone(snap)
	c.havePrev = true
	if c.onChange != nil {
		c.onChange(snap, delta, c.seq)
	}
}

// Snapshot returns the current composed FleetSnapshot, with every
// provider/node slot's staleness re-evaluated against clock.Now() and ttl
// at this exact call (fail-closed on stale data: a slot that has not been
// refreshed within ttl reports StateUnknown/PresenceUnknown here even if
// no Update* call has run since it went stale).
func (c *Compositor) Snapshot() FleetSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := c.buildSnapshotLocked()
	snap.Seq = c.seq
	return snap
}

// buildSnapshotLocked builds a fresh FleetSnapshot from the live maps,
// applying TTL expiry. Must be called with c.mu held (read or write).
func (c *Compositor) buildSnapshotLocked() FleetSnapshot {
	now := c.clock.Now()
	providers := make(map[string]ProviderSlot, len(c.providers))
	for name, slot := range c.providers {
		providers[name] = expireProviderSlot(slot, now, c.ttl)
	}
	nodeSlots := make(map[string]NodeSlot, len(c.nodes))
	for id, slot := range c.nodes {
		nodeSlots[id] = expireNodeSlot(slot, now, c.ttl)
	}
	return FleetSnapshot{GeneratedAt: now, Providers: providers, Nodes: nodeSlots}
}
