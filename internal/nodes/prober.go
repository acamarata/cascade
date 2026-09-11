// Purpose: the presence prober: a 30 s injected-clock probe loop over
//	every enrolled device record's heartbeat/tunnel liveness, plus the
//	out-of-cycle trigger netwatch.go fires on a detected network change.
// Inputs: RecordStore's persisted DeviceRecords, the injected Clock and
//	Ticker, and a ProbeFunc that reports each record's liveness over the
//	enrolled node channel (production: HeartbeatProbe, reading the same
//	authenticated LastSeen field ProcessHeartbeat writes; this ticket
//	builds no second wire transport — the heartbeat/tunnel channel S-36.T2/
//	T3 already authenticate IS the enrolled node channel R-16.37 asks the
//	prober to probe).
// Outputs: an updated, persisted DeviceRecord.Presence per node and a
//	node.presence.changed event on every real transition.
// Constraints: no bare time.Now/time.Sleep (Art.7.3) — every tick comes
//	from the injected Ticker; an unauthenticated or stale probe result is
//	never evidence (presence.go's AdvancePresence enforces this, this
//	file never bypasses it); the remote-via-route leg is an injectable
//	RouteChecker, defaulting to always-false (defaultRouteChecker) unless
//	the caller supplies travel.go's real NewSSHRouteChecker (S-72.T3),
//	consulted only once a record's direct miss streak would reach
//	ProbeMissThreshold AND the record carries a configured route —
//	never on the first miss.
// SPORT: internal/nodes Prober/ADDED, RouteChecker/ADDED, HeartbeatProbe/
//	ADDED (P1-E36-W7-S72-T2); route-gating change P1-E36-W7-S72-T3.

package nodes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

// RouteChecker resolves whether nodeID is reachable only via a
// configured route/tunnel, once its direct probe has already failed.
// defaultRouteChecker (always false) remains ProberDeps' zero-value
// default; travel.go's NewSSHRouteChecker (P1-E36-W7-S72-T3) is the real
// implementation, dialing through the same tunnel Dialer the ssh
// transport already uses. No production composition root constructs a
// Prober with either checker yet (this ticket's journal and
// internal/build/testonly-allow.json name the pre-existing gap).
type RouteChecker interface {
	Reachable(ctx context.Context, nodeID string) bool
}

type defaultRouteChecker struct{}

func (defaultRouteChecker) Reachable(context.Context, string) bool { return false }

// ProbeOutcome is one direct-probe attempt's result against the enrolled
// node channel.
type ProbeOutcome struct {
	// Reachable is true for fresh evidence (heartbeat within one probe
	// interval of now) — a hit.
	Reachable bool
	// NoEvidence is true when the channel has nothing at all to report —
	// no heartbeat ever, or one older than the full timeout window. This
	// is R-21.65's distinct "heartbeat-stream timeout, no probe
	// evidence" signal, resolved via ResolveHeartbeatTimeoutPresence
	// rather than AdvancePresence's miss path.
	NoEvidence bool
	At         time.Time
}

// ProbeFunc probes one enrolled record's liveness. HeartbeatProbe is the
// production implementation; tests inject a fake to drive every
// hysteresis/error-path case deterministically.
type ProbeFunc func(ctx context.Context, rec DeviceRecord, now time.Time) ProbeOutcome

// HeartbeatProbe is the production ProbeFunc: it reports Reachable when
// rec's LastSeen is within one DefaultProbeInterval of now (a fresh
// heartbeat — a hit), NoEvidence when LastSeen is zero or older than
// timeout (nothing to report at all), and otherwise a miss (LastSeen
// exists but has aged past one probe interval without going fully
// stale) — the three-way split AdvancePresence/ResolveHeartbeatTimeoutPresence
// need. Every result here is authenticated: it is derived from
// LastSeen, which only ProcessHeartbeat's signature-verified path ever
// writes (heartbeat.go) — never from an unauthenticated LAN reply.
func HeartbeatProbe(_ context.Context, rec DeviceRecord, now time.Time) ProbeOutcome {
	if rec.LastSeen.IsZero() {
		return ProbeOutcome{NoEvidence: true, At: now}
	}
	age := now.Sub(rec.LastSeen)
	if age < 0 || age > DefaultHeartbeatTimeout {
		return ProbeOutcome{NoEvidence: true, At: now}
	}
	return ProbeOutcome{Reachable: age <= DefaultProbeInterval, At: now}
}

// EventBus is the minimal seam Prober publishes through, duck-typed
// against *events.Bus's own Publish signature (mirrors
// internal/fleet/capacity.EventBus's identical precedent) so tests never
// require a real Bus.
type EventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// nodesNamespace and NodePresenceChangedKind are the node.presence.changed
// event's namespace/kind (R-16.67, task 5).
const (
	nodesNamespace                           = "nodes"
	NodePresenceChangedKind events.EventKind = "node.presence.changed"
)

// ProberDeps carries every collaborator the probe loop needs.
type ProberDeps struct {
	Records      *RecordStore
	Clock        Clock
	Ticker       Ticker
	Probe        ProbeFunc    // nil defaults to HeartbeatProbe
	RouteChecker RouteChecker // nil defaults to defaultRouteChecker{}
	Timeout      time.Duration
	Bus          EventBus // nil is a documented no-op
}

// Prober runs the R-16.37 30 s probe loop plus the out-of-cycle trigger
// netwatch.go fires on a detected network change.
type Prober struct {
	deps       ProberDeps
	outOfCycle chan struct{}
}

// NewProber returns a ready Prober over deps, applying HeartbeatProbe/
// defaultRouteChecker defaults for any nil seam.
func NewProber(deps ProberDeps) *Prober {
	if deps.Probe == nil {
		deps.Probe = HeartbeatProbe
	}
	if deps.RouteChecker == nil {
		deps.RouteChecker = defaultRouteChecker{}
	}
	if deps.Timeout <= 0 {
		deps.Timeout = DefaultHeartbeatTimeout
	}
	return &Prober{deps: deps, outOfCycle: make(chan struct{}, 1)}
}

// TriggerOutOfCycle requests an immediate probe pass on the next Run
// select, without waiting for the next 30 s tick (netwatch.go's caller on
// a detected network change). Non-blocking: a trigger already pending is
// coalesced, never queued.
func (p *Prober) TriggerOutOfCycle() {
	select {
	case p.outOfCycle <- struct{}{}:
	default:
	}
}

// Run drives the probe loop until ctx is canceled: one pass per
// deps.Ticker tick, plus one pass per TriggerOutOfCycle call.
func (p *Prober) Run(ctx context.Context) {
	defer p.deps.Ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.deps.Ticker.C():
			p.probeAll(ctx)
		case <-p.outOfCycle:
			p.probeAll(ctx)
		}
	}
}

// probeAll runs one pass over every enrolled device record. A List
// error is not fatal to the loop — it is reported nowhere but simply
// retried next tick, matching RunHeartbeatLoop's identical
// never-crash-on-one-failure convention.
func (p *Prober) probeAll(ctx context.Context) {
	recs, err := p.deps.Records.List()
	if err != nil {
		return
	}
	now := p.deps.Clock.Now()
	for _, rec := range recs {
		p.probeOne(ctx, rec, now)
	}
}

func (p *Prober) probeOne(ctx context.Context, rec DeviceRecord, now time.Time) {
	from := rec.Presence
	outcome := p.deps.Probe(ctx, rec, now)

	var updated DeviceRecord
	var transitioned bool
	switch {
	case outcome.NoEvidence:
		updated, transitioned = ResolveHeartbeatTimeoutPresence(rec, now, p.deps.Timeout)
	// The route leg fires only once this would be the ProbeMissThreshold-
	// th consecutive direct miss (R-16.37 §Nodes: "after three
	// consecutive direct-probe misses"), and only for a record carrying
	// a configured route (S-72.T3, travel.go) — never on the first miss,
	// and never for a node with no route to try.
	case !outcome.Reachable && rec.Route.configured() && rec.ConsecutiveMisses+1 >= ProbeMissThreshold &&
		p.deps.RouteChecker.Reachable(ctx, rec.NodeID):
		updated, transitioned = AdvancePresence(rec, PresenceObservation{At: outcome.At, Authenticated: true, RouteOnly: true}, now)
	default:
		updated, transitioned = AdvancePresence(rec, PresenceObservation{OK: outcome.Reachable, At: outcome.At, Authenticated: true}, now)
	}

	if !transitioned {
		_ = p.deps.Records.put(updated)
		return
	}
	if err := p.deps.Records.put(updated); err != nil {
		return
	}
	p.emitPresenceChanged(ctx, rec.NodeID, from, updated.Presence)
}

func (p *Prober) emitPresenceChanged(ctx context.Context, nodeID string, from, to Presence) {
	if p.deps.Bus == nil {
		return
	}
	payload := presenceChangedPayload{NodeID: nodeID, From: from, To: to}
	raw, err := marshalPresenceChanged(payload)
	if err != nil {
		return
	}
	_, _ = p.deps.Bus.Publish(ctx, nodesNamespace, NodePresenceChangedKind, "nodes.prober", raw)
}

// presenceChangedPayload is node.presence.changed's wire shape.
type presenceChangedPayload struct {
	NodeID string   `json:"node_id"`
	From   Presence `json:"from"`
	To     Presence `json:"to"`
}

func marshalPresenceChanged(p presenceChangedPayload) ([]byte, error) { return json.Marshal(p) }

// publishPresenceChanged is heartbeat.go's call-site for the same
// node.presence.changed event Prober.emitPresenceChanged publishes, so a
// transition detected synchronously on receipt of a real heartbeat
// (heartbeat.go) and one detected on the prober's own periodic tick
// (this file) both reach the bus through one shared encoding.
func publishPresenceChanged(ctx context.Context, bus EventBus, nodeID string, from, to Presence) {
	if bus == nil || from == to {
		return
	}
	raw, err := marshalPresenceChanged(presenceChangedPayload{NodeID: nodeID, From: from, To: to})
	if err != nil {
		return
	}
	_, _ = bus.Publish(ctx, nodesNamespace, NodePresenceChangedKind, "nodes.heartbeat", raw)
}
