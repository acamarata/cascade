package runtime

// Purpose: the two outward seams the hot-reload engine and baseline
//
//	checker call through — EventPublisher (C/S-04.T3's event bus) and
//	AuditRecorder (the audit domain via B/S-02's pkg/provider.Store) —
//	plus this ticket's own working implementations of both.
//
// Inputs: an event name + payload map (EventPublisher), or a record kind
//   - payload map (AuditRecorder).
//
// Outputs: none beyond error propagation; both are fire-and-forget from
//
//	the caller's perspective but never silently swallow an error — the
//	caller decides whether a publish/record failure should itself fail
//	the reload.
//
// Constraints: SEAM NOTE — internal/events (C/S-04.T3) and a concrete
//
//	internal/audit domain wiring (B/S-02's Store consumer) are both
//	still empty placeholder packages in this tree (verified: doc.go only,
//	no exported symbols) as of this ticket. This ticket does not own
//	either package (files_scope forbids it) and does not invent their
//	implementations; it defines the consumer-side interfaces at the
//	point of use — ordinary Go practice — and ships DiscardEventPublisher
//	plus a real, working StoreAuditRecorder against the ALREADY-REAL
//	pkg/provider.Store interface (verified: full Get/Put/Delete/Scan/Tx
//	contract, not a stub). Production wiring of a genuine event-bus
//	implementation behind EventPublisher, and of a genuine driver behind
//	provider.Store, both land with their owning tickets (C/S-04.T3,
//	B/S-02's driver ticket); HotReloader/BaselineChecker work correctly
//	today against DiscardEventPublisher + StoreAuditRecorder(storetest.MemStore)
//	and will work identically, with zero code change, the day a real Bus
//	is threaded in — this is the intended shape of the seam, not a
//	temporary workaround.
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/provider"
)

// EventPublisher is the seam HotReloader publishes config.reload.accepted /
// config.reload.rejected / config.restart.required / config.policy.divergent
// events through. Its production implementation is C/S-04.T3's event bus;
// this package only depends on the interface.
type EventPublisher interface {
	Publish(ctx context.Context, name string, payload map[string]interface{})
}

// DiscardEventPublisher discards every event. It is the default for
// daemonless / not-yet-wired callers (e.g. `cascade config set` running
// outside a daemon, or any caller constructed before C/S-04.T3 lands) —
// using it never causes an error, it only means the event is not observed
// anywhere; Reload's own return value and the AuditRecorder are always the
// authoritative record of what happened regardless of whether a real bus
// is attached.
type DiscardEventPublisher struct{}

// Publish implements EventPublisher by discarding the event.
func (DiscardEventPublisher) Publish(context.Context, string, map[string]interface{}) {}

// auditNamespace is the pkg/provider.Store namespace every audit record
// this ticket writes lands in (R-14.5's ten-domain cascade.db contract
// names "audit" as one of the ten; R-14.8 requires config baseline +
// reload/rejection records to persist there via the Store API).
const auditNamespace = "audit"

// AuditRecorder is the seam HotReloader/BaselineChecker persist
// security-relevant records through: every accepted/rejected reload, every
// boot-time baseline outcome, and every doctor-error. Its production
// backing store is the audit domain (R-14.8); this package only depends on
// the interface.
type AuditRecorder interface {
	Record(ctx context.Context, kind string, fields map[string]interface{}) error
}

// StoreAuditRecorder is the real (non-stub) AuditRecorder implementation,
// backed directly by pkg/provider.Store — the actual B/S-02 Store API
// contract, not a placeholder. Each record is JSON-encoded and written
// under a key namespaced by kind, so `Scan(ctx, "audit", kind+"/")`
// retrieves every record of one kind in insertion order (the counter is
// zero-padded for lexicographic == chronological ordering within one
// process's lifetime).
type StoreAuditRecorder struct {
	store   provider.Store
	clock   Clock
	counter atomic.Uint64
}

// NewStoreAuditRecorder builds a StoreAuditRecorder over store, using
// clock for each record's Time field (never a bare time.Now(); Art.7.3).
func NewStoreAuditRecorder(store provider.Store, clock Clock) *StoreAuditRecorder {
	return &StoreAuditRecorder{store: store, clock: clock}
}

// auditRecord is the JSON envelope every StoreAuditRecorder entry is
// encoded as.
type auditRecord struct {
	Kind   string                 `json:"kind"`
	TimeMS int64                  `json:"time_ms"`
	Fields map[string]interface{} `json:"fields"`
}

// Record persists one audit entry. A nil store (never wired — e.g. a
// standalone `cascade config set` invocation with no daemon/store
// available) is treated as a no-op success: audit persistence is
// best-effort infrastructure the caller may not always have, and a
// missing store must never itself block a config write or reload that is
// otherwise valid.
func (r *StoreAuditRecorder) Record(ctx context.Context, kind string, fields map[string]interface{}) error {
	if r == nil || r.store == nil {
		return nil
	}
	seq := r.counter.Add(1)
	rec := auditRecord{Kind: kind, TimeMS: r.clock.Now().UnixMilli(), Fields: fields}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := auditKey(kind, seq)
	return r.store.Put(ctx, auditNamespace, key, data)
}

// auditKey builds the deterministic, sortable key for one audit record.
func auditKey(kind string, seq uint64) string {
	const width = 20 // enough digits for any uint64, zero-padded for sort order
	digits := []byte{}
	n := seq
	for i := 0; i < width; i++ {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return kind + "/" + string(digits)
}
