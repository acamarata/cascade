// Purpose: the R-16.60b producer and consumer APIs: Notifier.Deliver (the
//
//	direct producer entry point used by CI fan-out, delegation results,
//	and future callers) and Inbox.List/Read/Ack/Expire (the query
//	surface AK/S-73.T2 and the MCP inbox tools consume).
//
// Inputs: Deliver takes a caller-populated Notification; List/Read/Ack
//
//	take a SessionScopeRef identifying the querying session.
//
// Outputs: Deliver assigns Timestamp from the injected clock and enqueues
//
//	by priority, returning a typed error for missing required scope
//	fields; List/Read/Ack apply ScopeDeliveryPredicate and Visibility
//	first, so a withheld record is never discoverable by id.
//
// Constraints: R-21.227 — the Inbox is a PROJECTION over its producers'
//
//	stores, not the record of truth. It persists nothing across a
//	daemon restart; a producer needing pending-across-restart semantics
//	owns its own durable table and re-Delivers on daemon start (named
//	owner for executive decision packets: AP/S-81.T3, out of this
//	ticket's scope). No documentation here claims durability (Art.1.3).
//
// SPORT: internal.notify.Notifier/ADDED, internal.notify.Inbox/ADDED
//
//	(P1-E23-W5-S49-T1).

package notify

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Typed A-T7 results for the Inbox query surface.
var (
	// ErrNotificationUnknown reports that no Notification with the given
	// id has ever been recorded by this Inbox.
	ErrNotificationUnknown = cascade.New(cascade.KindNotFound, "notify: unknown notification id")
	// ErrNotificationWithheld reports that a Notification with the given
	// id exists but ScopeDeliveryPredicate or Visibility refuses it to
	// the querying session — indistinguishable from unknown in every
	// other observable respect (fail closed: no existence leak).
	ErrNotificationWithheld = cascade.New(cascade.KindPermissionDenied, "notify: notification withheld from this session")
	// ErrNotificationExpired reports that the Notification's ExpiresAt is
	// at-or-before the current clock reading.
	ErrNotificationExpired = cascade.New(cascade.KindNotFound, "notify: notification expired")
	// ErrNotificationAlreadyAcked reports that Ack was already called for
	// this id by this session.
	ErrNotificationAlreadyAcked = cascade.New(cascade.KindConflict, "notify: notification already acknowledged")
	// errMissingScopeFields is Deliver's validation failure for a
	// Notification whose Class requires scope fields it does not carry.
	errMissingScopeFields = cascade.New(cascade.KindInvalidInput, "notify: notification missing required scope fields for its class")
)

// SessionScopeRef identifies the querying session for the Inbox surface:
// its own session ID plus its precomputed candidate scope set. It is the
// same shape ScopeDeliveryPredicate consumes (scope.go) — the query
// surface and the dispatch-time gate apply one identical predicate.
type SessionScopeRef = CandidateSession

// Counts summarizes one Inbox.List call's population: how many records
// matched the query, how many are unread, and — for observability, never
// silently discarded — how many were withheld from or expired for the
// querying session.
type Counts struct {
	Total    int
	Unread   int
	Withheld int
	Expired  int
}

// inboxOpts narrows an Inbox.List call.
type inboxOpts struct {
	// All expands the query to the `--all` global view (every
	// Notification, subject still to ScopeDeliveryPredicate/Visibility).
	All bool
	// Unread filters to Notifications this session has not yet Acked.
	Unread bool
}

// InboxOpts is the caller-facing alias for inboxOpts (kept as a named type
// so call sites read InboxOpts{...} rather than an anonymous struct).
type InboxOpts = inboxOpts

// inboxRecord is one stored Notification plus its per-session ack state.
// The Inbox holds no other persistence: R-21.227 makes it a projection,
// not a record of truth.
type inboxRecord struct {
	n       Notification
	ackedBy map[string]bool
	expired bool
}

// Inbox is the live, in-memory query surface over every Notification the
// dispatch loop has processed. It survives only for the daemon process's
// lifetime.
type Inbox struct {
	mu             sync.RWMutex
	records        map[string]*inboxRecord
	withheldCounts map[string]int // sessionID -> withheld count
}

// NewInbox returns an empty Inbox.
func NewInbox() *Inbox {
	return &Inbox{
		records:        make(map[string]*inboxRecord),
		withheldCounts: make(map[string]int),
	}
}

// record stores notif (called by the dispatch loop after every fan-out
// attempt, delivered or not).
func (ib *Inbox) record(notif Notification) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if existing, ok := ib.records[notif.ID]; ok {
		existing.n = notif
		return
	}
	ib.records[notif.ID] = &inboxRecord{n: notif, ackedBy: make(map[string]bool)}
}

// recordWithheld increments sessionID's withheld count for notif without
// making notif discoverable to that session.
func (ib *Inbox) recordWithheld(notif Notification, sessionID string) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if _, ok := ib.records[notif.ID]; !ok {
		ib.records[notif.ID] = &inboxRecord{n: notif, ackedBy: make(map[string]bool)}
	}
	ib.withheldCounts[sessionID]++
}

// recordExpired marks notif as expired without fanning it out.
func (ib *Inbox) recordExpired(notif Notification) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	rec, ok := ib.records[notif.ID]
	if !ok {
		rec = &inboxRecord{n: notif, ackedBy: make(map[string]bool)}
		ib.records[notif.ID] = rec
	}
	rec.expired = true
}

// List returns every Notification visible to session, subject to opts,
// plus Counts summarizing the full population (including withheld/expired
// records this session cannot see individually).
func (ib *Inbox) List(_ context.Context, session SessionScopeRef, opts InboxOpts) ([]Notification, Counts) {
	ib.mu.RLock()
	defer ib.mu.RUnlock()

	var out []Notification
	counts := Counts{Withheld: ib.withheldCounts[session.SessionID]}
	for _, rec := range ib.records {
		counts.Total++
		if rec.expired {
			counts.Expired++
			continue
		}
		if !visible(session, rec) {
			continue
		}
		acked := rec.ackedBy[session.SessionID]
		if !acked {
			counts.Unread++
		}
		if opts.Unread && acked {
			continue
		}
		out = append(out, rec.n)
	}
	return out, counts
}

// Read returns one Notification by id, if session may discover it.
func (ib *Inbox) Read(ctx context.Context, session SessionScopeRef, id string) (Notification, error) {
	if err := ctx.Err(); err != nil {
		return Notification{}, cascade.Wrap(cascade.KindCanceled, err, "notify: Read canceled")
	}
	ib.mu.RLock()
	defer ib.mu.RUnlock()
	rec, ok := ib.records[id]
	if !ok {
		return Notification{}, ErrNotificationUnknown
	}
	if rec.expired {
		return Notification{}, ErrNotificationExpired
	}
	if !visible(session, rec) {
		return Notification{}, ErrNotificationWithheld
	}
	return rec.n, nil
}

// Ack marks id acknowledged for session.
func (ib *Inbox) Ack(ctx context.Context, session SessionScopeRef, id string) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "notify: Ack canceled")
	}
	ib.mu.Lock()
	defer ib.mu.Unlock()
	rec, ok := ib.records[id]
	if !ok {
		return ErrNotificationUnknown
	}
	if rec.expired {
		return ErrNotificationExpired
	}
	if !visible(session, rec) {
		return ErrNotificationWithheld
	}
	if rec.ackedBy[session.SessionID] {
		return ErrNotificationAlreadyAcked
	}
	rec.ackedBy[session.SessionID] = true
	return nil
}

// Expire sweeps every not-yet-expired record whose ExpiresAt is
// at-or-before now into the expired count, so List/Read stop surfacing
// it. It returns the number newly swept. The dispatch loop already
// expires opportunistically at fan-out time (fanOut->recordExpired); this
// is the sweep for records that were never re-drained (a queue that
// stayed empty after their ExpiresAt passed).
func (ib *Inbox) Expire(now time.Time) int {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	swept := 0
	for _, rec := range ib.records {
		if rec.expired {
			continue
		}
		if rec.n.expired(now) {
			rec.expired = true
			swept++
		}
	}
	return swept
}

// Notifier is the R-16.60b direct producer entry point: Deliver validates
// the R-16.5 scope fields, assigns Timestamp from the injected clock, and
// enqueues by priority — the same path event-bus-decoded notifications
// take (router.go).
type Notifier struct {
	queues *queueSet
	clock  runtime.Clock
}

// NewNotifier returns a Notifier enqueueing into queues, timestamping with
// clock.
func NewNotifier(queues *queueSet, clock runtime.Clock) *Notifier {
	return &Notifier{queues: queues, clock: clock}
}

// Deliver validates n's required scope fields for its resolved Class,
// assigns Timestamp, and enqueues n by Priority. It returns
// errMissingScopeFields for an Addressed Notification with no
// TargetSession, or a Scoped Notification with neither OriginScope nor
// TargetScope set.
func (nf *Notifier) Deliver(ctx context.Context, n Notification) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "notify: Deliver canceled")
	}
	class := n.Class.Resolve()
	switch class {
	case ClassAddressed:
		if n.TargetSession == "" {
			return errMissingScopeFields
		}
	case ClassScoped:
		if n.OriginScope == "" && n.TargetScope == "" {
			return errMissingScopeFields
		}
	case ClassGlobalCritical, classUnresolvable:
		// GlobalCritical needs no scope fields; classUnresolvable is
		// listed only because Resolve()'s return type still enumerates
		// it — Resolve() never actually produces it here.
	}
	n.Timestamp = nf.clock.Now()
	nf.queues.enqueue(n)
	return nil
}
