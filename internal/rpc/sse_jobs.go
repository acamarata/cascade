package rpc

// Purpose: the six P1-E29-W6-S60-T1 job/lease lifecycle SSE event kinds,
//	registered against internal/rpc/sse.go's existing exact-match filter
//	mechanism (parseFilter/filterSet), plus CombineKnownEventKind, the
//	composition-root helper that lets a daemon's SSEHandler accept both
//	its own kinds and these six without either predicate knowing about
//	the other. Split from sse.go purely to keep that file under the
//	300-line cap (Art.10.3) — this is registration content sse.go's own
//	filter mechanism owns, not a second parser.
//
// SPORT: internal.rpc.SSEHandler/ADDED job/lease topics (P1-E29-W6-S60-T1).

import "github.com/acamarata/cascade/internal/events"

// The six P1-E29-W6-S60-T1 job/lease lifecycle event kinds, registered
// with this file's existing exact-match filter mechanism (parseFilter/
// filterSet above) — no second parser is added for them.
//
// CONTRACT NOTE (files_scope, quoted in the journal): the full_desc names
// a "job:*"/"lease:*" GLOB filter syntax ("?filter=job:*"). This file's
// real, already-shipped filter (parseFilter/filterSet, above) is EXACT
// EventKind-string membership, comma-separated — there is no glob
// matching anywhere in it, and adding one here would be exactly the
// "second filter parser" this ticket's own HOW step 2 forbids (K/S-23.T3
// owns that parser). A client that wants every job/lease kind requests
// them by their literal names,
// e.g. "?filter=job.leased,job.transitioned,job.completed,job.failed" —
// not a "job:*" wildcard. Reusing the SAME six constants below on the
// production side prevents that literal set from drifting.
//
// A second, disclosed gap: no production caller in this tree publishes
// EventJobLeased/Transitioned/Completed/Failed today (grep confirms only
// jobs.EventLeaseAcquired="jobs.lease.acquired" and
// jobs.EventLeaseExpired="jobs.lease.expired" are ever published, under
// different literal strings than these six, and to the "jobs.lease" bus
// namespace, not whichever single namespace a daemon's SSEHandler binds
// to). Wiring that production emission is scheduler/admission logic this
// ticket's own acceptance criteria excludes ("No scheduler logic (T5)...
// added"); sse_filter_test.go proves this file's REAL SSEHandler
// correctly delivers/excludes these six kinds against synthetic
// bus.Publish calls, which is the registration this ticket owns. Wiring
// a real producer, and reconciling the "jobs.lease" vs. these six kinds'
// namespace, is left as an explicit, named gap for whichever ticket adds
// job/lease lifecycle publishing.
const (
	EventJobLeased        events.EventKind = "job.leased"
	EventJobTransitioned  events.EventKind = "job.transitioned"
	EventJobCompleted     events.EventKind = "job.completed"
	EventJobFailed        events.EventKind = "job.failed"
	EventLeaseAcquiredSSE events.EventKind = "lease.acquired"
	EventLeaseExpiredSSE  events.EventKind = "lease.expired"
)

// jobLeaseEventKinds lists the six constants above, for
// KnownJobLeaseEventKind and sse_filter_test.go's table.
var jobLeaseEventKinds = []events.EventKind{
	EventJobLeased, EventJobTransitioned, EventJobCompleted, EventJobFailed,
	EventLeaseAcquiredSSE, EventLeaseExpiredSSE,
}

// KnownJobLeaseEventKind reports whether kind is one of the six
// registered job/lease kinds — a KnownEventKind predicate the
// composition root combines with its own via CombineKnownEventKind.
func KnownJobLeaseEventKind(kind events.EventKind) bool {
	for _, k := range jobLeaseEventKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// CombineKnownEventKind ORs any number of KnownEventKind predicates into
// one, so a composition root can register this ticket's six kinds
// alongside whatever predicate it already had (e.g. daemon's own
// shutdown-requested kind) without either predicate needing to know
// about the other.
func CombineKnownEventKind(preds ...KnownEventKind) KnownEventKind {
	return func(kind events.EventKind) bool {
		for _, p := range preds {
			if p != nil && p(kind) {
				return true
			}
		}
		return false
	}
}
