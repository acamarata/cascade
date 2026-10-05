// Purpose: the fan-out retention backstop (contract:fanout-producer, SCAN
//   and SWEEP): Sweep deletes the records of every in-scope fan-out whose
//   last journal entry is older than the ttl (writing an "expired" marker
//   when none exists), removes the residue a crash left between a final
//   marker and its deletes, and reaps request records with no entity. The
//   records are the leg results, the conductor.fanout.attempts slots (all
//   of them once the final marker is read back; before that only a
//   finished leg's, DeleteTask) and the request record.
//   It also holds the per-fan-out state reader Scan, Sweep and the producer
//   share.
// Inputs: FanOutDeps (daemon store, head reader, claim table, clock), the
//   sweep instant and the ttl.
// Outputs: the ids it expired and an error joining every per-entity
//   failure (one bad entity never stops the others).
// Constraints: in scope are only "fanout:<id>" entities whose seq 1 is our
//   cursor naming <id>; every other entity is never read past the prefix
//   check and never written. Each entity is worked on only while its bare
//   id is claimed, and the claim is released on every path.
// SPORT: internal.fleet.resume.Sweep/ADDED (P1-CORE-19).

package resume

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// HeadReader reads a journal entity's head sequence (0: never appended).
// *journal.SQLiteStore satisfies it.
type HeadReader interface {
	HeadSeq(ctx context.Context, entityID string) (uint64, error)
}

// FanOutDeps are Scan's and Sweep's collaborators. Every field is required.
type FanOutDeps struct {
	Store  provider.Store
	Heads  HeadReader
	Claims *Claims
	Clock  runtime.Clock
}

// journal builds the fleet journal over d.Store, or ErrConstructionFailed
// when any collaborator is missing (a nil claim table is never read as
// "nothing claimed").
func (d FanOutDeps) journal() (journal.Store, error) {
	if d.Store == nil || d.Heads == nil || d.Claims == nil || d.Clock == nil {
		return nil, ErrConstructionFailed
	}
	return journal.New(d.Store, d.Clock, journal.DefaultNamespace), nil
}

// FanOutState is what one fan-out entity's journal says.
type FanOutState struct {
	// Ours is true only when seq 1 is our cursor naming this fan-out id.
	Ours      bool
	Cursor    FanOutCursor
	Final     string                  // final-marker outcome, "" when unmarked
	Completed map[int]conductor.JobID // legs with an ok done
	Starts    map[int]int             // raw fanout_leg_started count per leg
	Terminal  bool                    // a failed_terminal done exists
	Last      time.Time               // instant of the last entry
}

// State reads fanoutID's entity.
func (s *FanOutStore) State(ctx context.Context, fanoutID string) (FanOutState, error) {
	return loadState(ctx, s.journal, fanoutID)
}

// loadState replays FanOutEntity(fanoutID) into a FanOutState. A
// non-cursor seq 1 leaves Ours false and nothing else decoded.
func loadState(ctx context.Context, js journal.Store, fanoutID string) (FanOutState, error) {
	entity := FanOutEntity(fanoutID)
	entries, err := js.Replay(ctx, entity, journal.Cursor{EntityID: entity, Seq: 0}, nil)
	if err != nil {
		return FanOutState{}, rewrap(err, "resume: reading fan-out entity")
	}
	st := FanOutState{Completed: map[int]conductor.JobID{}, Starts: map[int]int{}}
	if len(entries) == 0 || !ownCursor(entries[0], fanoutID, &st.Cursor) {
		return st, nil
	}
	st.Ours = true
	st.Last = entries[len(entries)-1].Time()
	for _, e := range entries[1:] {
		if err := st.apply(e); err != nil {
			return FanOutState{}, err
		}
	}
	return st, nil
}

// ownCursor reports whether e is seq 1 holding our cursor for fanoutID,
// filling c when it is.
func ownCursor(e journal.Entry, fanoutID string, c *FanOutCursor) bool {
	var p resumeCursorPayload
	if e.Seq != 1 || e.Kind != journal.KindResumeCursor || json.Unmarshal(e.Payload, &p) != nil ||
		p.T != "cursor" || p.FanOutID != fanoutID || p.Legs < 1 {
		return false
	}
	*c = FanOutCursor{FanOutID: p.FanOutID, TaskID: p.TaskID, Legs: p.Legs, RequestDigest: p.RequestDigest, RequestKey: p.RequestKey}
	return true
}

// apply folds one post-cursor entry into st. An undecodable leg or final
// payload fails closed.
func (st *FanOutState) apply(e journal.Entry) error {
	switch e.Kind {
	case journal.KindFanOutLegStarted, journal.KindFanOutLegDone:
		var p legPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return ErrUnrecognizedShape
		}
		if e.Kind == journal.KindFanOutLegStarted {
			st.Starts[p.LegIndex]++
			return nil
		}
		switch p.Outcome {
		case conductor.LegOutcomeOK:
			st.Completed[p.LegIndex] = conductor.JobID(p.JobID)
		case conductor.LegOutcomeFailedTerminal:
			st.Terminal = true
		}
	case journal.KindIntent, journal.KindCheckpoint, journal.KindEscalation, journal.KindResumeCursor, journal.KindNodeStream:
		return nil // not a fan-out leg or final entry (fence markers ride KindResumeCursor)
	case journal.KindAck:
		var m finalMarker
		if json.Unmarshal(e.Payload, &m) == nil && m.T == "fanout_final" && st.Final == "" {
			st.Final = m.Outcome
		}
	}
	return nil
}

// FanOutClass is Scan's verdict for one in-scope fan-out.
type FanOutClass uint8

// Scan verdicts (the EPIC outcome table).
const (
	FanOutResumable           FanOutClass = iota + 1 // record kept; waits for the client's re-attach
	FanOutCompleteUndelivered                        // every leg ok, never delivered; re-attach returns it
	FanOutTerminal                                   // finalized terminal by this scan, records deleted
	FanOutFinalMarked                                // already final; residue deleted, never resumed
)

// ErrRequestRecordMissing is the Terminal verdict for a cursor whose
// request record does not exist.
var ErrRequestRecordMissing = cascade.New(cascade.KindNotFound, "resume: fan-out cursor has no request record; finalized terminal")

// FanOutScan is one in-scope fan-out's verdict. Request is set for
// FanOutResumable and FanOutCompleteUndelivered.
type FanOutScan struct {
	FanOutID string
	Class    FanOutClass
	Request  provider.ModelRequest
	Err      error
}

// Sweep is the backstop: see the file comment. now is the sweep instant
// (from the caller's injected clock) and ttl the record lifetime.
func Sweep(ctx context.Context, deps FanOutDeps, now time.Time, ttl time.Duration) ([]string, error) {
	js, err := deps.journal()
	if err != nil {
		return nil, err
	}
	entities, err := js.ListEntities(ctx)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "resume: listing journal entities for the sweep")
	}
	var expired []string
	var errs []error
	for _, entity := range entities {
		id, ok := FanOutIDFromEntity(entity)
		if !ok {
			continue // not a fan-out entity: never read, never written
		}
		didExpire, err := sweepOne(ctx, js, deps, id, now, ttl)
		if didExpire {
			expired = append(expired, id)
		}
		errs = append(errs, err)
	}
	errs = append(errs, reapOrphanRequests(ctx, deps))
	return expired, errors.Join(errs...)
}

// sweepOne sweeps one fan-out while holding its claim; a busy id is
// skipped untouched.
func sweepOne(ctx context.Context, js journal.Store, deps FanOutDeps, id string, now time.Time, ttl time.Duration) (bool, error) {
	if !deps.Claims.TryClaim(id) {
		return false, nil
	}
	defer deps.Claims.Release(id)
	st, err := loadState(ctx, js, id)
	if err != nil || !st.Ours {
		return false, err
	}
	switch {
	case st.Final != "":
		return false, deleteFinalized(ctx, js, deps.Store, id)
	case now.Sub(st.Last) <= ttl:
		return false, nil
	default:
		return true, finalize(ctx, js, deps.Store, id, OutcomeExpired)
	}
}

// deleteFinalized removes the records of a fan-out whose final marker was
// read back. A final fan-out never starts a leg again (the producer
// refuses it), so every conductor.fanout.attempts slot goes in one
// transaction whatever the leg result (R13 as widened); deleteRecords then
// removes the results and the request record. A store error leaves every
// slot and is returned (fails closed).
func deleteFinalized(ctx context.Context, js journal.Store, store provider.Store, id string) error {
	keys, err := listPrefix(ctx, store, legAttemptsNamespace, id+"#")
	if err != nil {
		return err
	}
	if len(keys) > 0 { // no write transaction when no slot is left
		err = store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
			for _, key := range keys {
				if err := tx.Delete(ctx, legAttemptsNamespace, key); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return rewrap(err, "resume: deleting the attempt slots of final fan-out "+id)
		}
	}
	return deleteRecords(ctx, js, store, id)
}

// reapOrphanRequests deletes a request record with key K only when
// FanOutEntity(K) has never been appended to (HeadSeq 0).
func reapOrphanRequests(ctx context.Context, deps FanOutDeps) error {
	keys, err := listKeys(ctx, deps.Store, requestsNamespace)
	if err != nil {
		return err
	}
	var errs []error
	for _, key := range keys {
		errs = append(errs, reapOne(ctx, deps, key))
	}
	return errors.Join(errs...)
}

// reapOne reaps one request record under its claim.
func reapOne(ctx context.Context, deps FanOutDeps, key string) error {
	if !deps.Claims.TryClaim(key) {
		return nil
	}
	defer deps.Claims.Release(key)
	head, err := deps.Heads.HeadSeq(ctx, FanOutEntity(key))
	if err != nil || head != 0 {
		return rewrap(err, "resume: reading fan-out head for the reap")
	}
	return rewrap(deps.Store.Delete(ctx, requestsNamespace, key), "resume: reaping orphan fan-out request record")
}

// listKeys lists every key of namespace.
func listKeys(ctx context.Context, store provider.Store, namespace string) ([]string, error) {
	return listPrefix(ctx, store, namespace, "")
}

// listPrefix lists every key of namespace that starts with prefix.
func listPrefix(ctx context.Context, store provider.Store, namespace, prefix string) ([]string, error) {
	it, err := store.Scan(ctx, namespace, prefix)
	if err != nil {
		return nil, rewrap(err, "resume: listing "+namespace)
	}
	var keys []string
	for it.Next(ctx) {
		keys = append(keys, it.Key())
	}
	return keys, errors.Join(rewrap(it.Err(), "resume: listing "+namespace), rewrap(it.Close(), "resume: closing the "+namespace+" listing"))
}
