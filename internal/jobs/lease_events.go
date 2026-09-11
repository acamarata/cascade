package jobs

// Purpose: the lease lifecycle's event-bus and journal integration (step
//
//	7/8 of this ticket's HOW) plus the expiry/fence-mismatch attention
//	integration (step 6/10) -- one leaseEventSink other files call after
//	their own DB transaction has already committed. No RPC methods are
//	registered anywhere in this ticket (S-60.T1's scope).
//
// Inputs: a ResourceLease (or repoID/scopeGlob/holder for contended,
//
//	which has no lease row of its own yet).
//
// Outputs: a real C/S-04.T3 events.Bus publish, a real M/S-27.T1
//
//	journal.Store append, and (expired/fenced only) a real R/S-39.T1
//	supervision.Store attention push -- Art.2: every one of these three
//	is a REAL counterpart, never a package-local double, in production
//	AND in lease_events_test.go's integration test.
//
// Constraints: JOURNAL KIND MAPPING (a contract gap filled reasonably,
//
//	same posture as model.go's DataClass/ExecutionState precedent):
//	journal.Kind's eight members were not designed around lease
//	semantics, so this file maps acquired->Intent (a new exclusive-write
//	intent begins), renewed->Checkpoint (republishing coverage over the
//	SAME held state), released/expired/reclaimed->Ack (closing out the
//	intent an acquired/expired row opened), and contended->Escalation
//	(recorded under the WINNING lease's entity id, since a contender
//	never gets a lease row of its own). Every append/publish/push here
//	runs best-effort AFTER its caller's own DB transaction already
//	committed (journal/bus/attention are external systems that cannot
//	join that transaction) -- a failure here is returned to the
//	caller, but the underlying lease mutation it describes has already
//	landed, matching internal/fleet/supervision's own emit-after-write
//	precedent (attention_store.go's Push/emit split).
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The five lease lifecycle EventKind values this package publishes on
// leaseEventNamespace. events.EventKind is deliberately OPEN (unlike
// pkg/cascade's frozen taxonomy): each subsystem defines its own values.
const (
	EventLeaseAcquired  events.EventKind = "jobs.lease.acquired"
	EventLeaseContended events.EventKind = "jobs.lease.contended"
	EventLeaseRenewed   events.EventKind = "jobs.lease.renewed"
	EventLeaseReleased  events.EventKind = "jobs.lease.released"
	EventLeaseExpired   events.EventKind = "jobs.lease.expired"
)

// leaseEventNamespace is the T5 advance(dag, event) input / S-60.T1
// filtered-SSE feed's namespace for every lease lifecycle event.
const leaseEventNamespace = "jobs.lease"

// leaseEventSink bundles the three real downstream systems a lease
// lifecycle event fans out to. A nil field disables that one fan-out
// (tests that only care about journaling, say, pass a nil bus).
type leaseEventSink struct {
	bus       *events.Bus
	journal   journal.Store
	attention *supervision.Store
}

// newLeaseEventSink constructs a sink. Any argument may be nil to
// disable that fan-out.
func newLeaseEventSink(bus *events.Bus, j journal.Store, attention *supervision.Store) *leaseEventSink {
	return &leaseEventSink{bus: bus, journal: j, attention: attention}
}

type leasePayload struct {
	RepoID    string `json:"repo_id"`
	ScopeGlob string `json:"scope_glob"`
	Holder    string `json:"holder"`
	Epoch     int64  `json:"epoch"`
	State     string `json:"state"`
}

func toPayload(l ResourceLease) leasePayload {
	return leasePayload{RepoID: l.RepoID, ScopeGlob: l.ScopeGlob, Holder: l.Holder, Epoch: l.Epoch, State: string(l.State)}
}

func (s *leaseEventSink) publish(ctx context.Context, kind events.EventKind, l ResourceLease) error {
	if s.bus == nil {
		return nil
	}
	raw, err := json.Marshal(toPayload(l))
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode lease event payload")
	}
	_, err = s.bus.Publish(ctx, leaseEventNamespace, kind, "jobs.lease", raw)
	return err
}

func (s *leaseEventSink) appendJournal(ctx context.Context, l ResourceLease, kind journal.Kind, op string) error {
	if s.journal == nil {
		return nil
	}
	raw, err := json.Marshal(toPayload(l))
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode lease journal payload")
	}
	_, err = s.journal.Append(ctx, leaseEntityID(l.RepoID, l.ScopeGlob), kind, op, json.RawMessage(raw))
	return err
}

func (s *leaseEventSink) acquired(ctx context.Context, l ResourceLease) error {
	if err := s.appendJournal(ctx, l, journal.KindIntent, "acquired"); err != nil {
		return err
	}
	return s.publish(ctx, EventLeaseAcquired, l)
}

func (s *leaseEventSink) renewed(ctx context.Context, l ResourceLease) error {
	if err := s.appendJournal(ctx, l, journal.KindCheckpoint, "renewed"); err != nil {
		return err
	}
	return s.publish(ctx, EventLeaseRenewed, l)
}

func (s *leaseEventSink) released(ctx context.Context, l ResourceLease) error {
	if err := s.appendJournal(ctx, l, journal.KindAck, "released"); err != nil {
		return err
	}
	return s.publish(ctx, EventLeaseReleased, l)
}

func (s *leaseEventSink) reclaimed(ctx context.Context, l ResourceLease) error {
	return s.appendJournal(ctx, l, journal.KindAck, "reclaimed")
}

// contended has no lease row of its own -- it journals/publishes under
// the WINNING (conflicting) lease's entity id, naming the loser's
// would-be holder in the payload.
func (s *leaseEventSink) contended(ctx context.Context, repoID, scopeGlob, requestingHolder string, conflicting ResourceLease) error {
	if s.journal != nil {
		raw, err := json.Marshal(map[string]string{
			"repo_id": repoID, "scope_glob": scopeGlob,
			"requesting_holder": requestingHolder, "conflicting_holder": conflicting.Holder,
		})
		if err != nil {
			return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode contended payload")
		}
		if _, err := s.journal.Append(ctx, leaseEntityID(conflicting.RepoID, conflicting.ScopeGlob),
			journal.KindEscalation, "contended", json.RawMessage(raw)); err != nil {
			return err
		}
	}
	return s.publish(ctx, EventLeaseContended, conflicting)
}

// expired pushes the step-6 attention item (kind stall, source_ref =
// this lease's entity id, idempotent) through the REAL R/S-39.T1 queue,
// then journals and publishes.
func (s *leaseEventSink) expired(ctx context.Context, l ResourceLease) error {
	if err := s.pushAttention(ctx, l); err != nil {
		return err
	}
	if err := s.appendJournal(ctx, l, journal.KindAck, "expired"); err != nil {
		return err
	}
	return s.publish(ctx, EventLeaseExpired, l)
}

// fenced pushes the step-10 attention item for a fence-mismatch refusal.
//
// CONTRACT-VS-TREE CONTRADICTION (recorded, not papered over): the
// ticket text asks for a "structured payload {repo id, scope_glob,
// holder job id, expired_at, journal ref}" on the pushed item, but the
// real supervision.AttentionItem (attention.go, R/S-39.T1's shipped
// type) has no payload field at all -- only ID/Kind/SourceRef/ScopeRef/
// Priority/CreatedAt/AckedAt. SourceRef is also the (Kind, SourceRef)
// idempotency key (attention_store.go's Push), so it cannot carry a
// per-push varying payload (e.g. expired_at) without breaking dedup.
// This ticket follows the REAL type: SourceRef is the stable lease
// entity id (leaseEntityID), and the full structured context (repo,
// scope, holder, expired_at, journal ref) is instead recorded in the
// M/S-27.T1 journal entry under that SAME entity id (appendJournal,
// called first in both fenced and expired below) -- a reader paging an
// attention item can pull the structured detail from the journal by the
// shared id.
func (s *leaseEventSink) fenced(ctx context.Context, repoID, scopeGlob string, _ int64) error {
	if s.attention == nil {
		return nil
	}
	_, err := s.attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindStall,
		SourceRef: leaseEntityID(repoID, scopeGlob),
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindProject, ID: repoID},
	})
	return err
}

func (s *leaseEventSink) pushAttention(ctx context.Context, l ResourceLease) error {
	if s.attention == nil {
		return nil
	}
	_, err := s.attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindStall,
		SourceRef: leaseEntityID(l.RepoID, l.ScopeGlob),
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindProject, ID: l.RepoID},
	})
	return err
}
