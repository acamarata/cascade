package memory

// Purpose: FileCandidateLedger, the files-first CandidateLedger. One
//   candidate is one file under {base}/candidates/{kind}/, written
//   atomically and read fail-closed, matching the record store beside it
//   rather than introducing a second way for memory state to live.
// Inputs: a base directory, the MemoryStore a promotion writes through, an
//   injected Clock, and an event sink.
// Outputs: candidate state on disk, durable records in the MemoryStore,
//   typed pkg/cascade errors, and one event per state transition.
// Constraints: no bare time.Now; no map iteration reaches a file; a
//   promotion is taken only from evidence that parsed whole.
// SPORT: G/memory-candidate-ledger (ADD, placeholder per T-1
//   sport_updates).

import (
	"context"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FileCandidateLedger records candidates as files beside the memory
// records they may become.
//
// Promotion is a one-way door: what it writes, the system afterwards
// treats as true about the user. Everything here is arranged so that door
// only opens on evidence that was read whole and counted deterministically
// - a candidate file that cannot be parsed is refused rather than treated
// as absent, session identity is a sorted set rather than a map walk, and
// the durable write happens before the candidate is marked promoted, so a
// crash between the two leaves a candidate that promotes again rather than
// one that claims a record it never wrote.
type FileCandidateLedger struct {
	base  string
	store MemoryStore
	clock Clock
	fs    fileSystem
	sink  CandidateEventSink
}

// Compile-time proof that the implementation satisfies the contract, so a
// drifting method set fails the build rather than a caller.
var _ CandidateLedger = (*FileCandidateLedger)(nil)

// NewFileCandidateLedger returns a ledger rooted at base, promoting through
// store and taking its timestamps from clk. Transitions are reported to
// sink; a nil sink discards them, which is the right default for a caller
// with no event bus wired and never silently changes what is stored.
func NewFileCandidateLedger(
	base string, store MemoryStore, clk Clock, sink CandidateEventSink,
) *FileCandidateLedger {
	return newCandidateLedgerWithFS(base, store, clk, sink, osFS{})
}

// newCandidateLedgerWithFS is NewFileCandidateLedger with the file-system
// seam supplied. Unexported: tests inject a failing file system through it,
// and no shipped path may substitute anything for osFS.
func newCandidateLedgerWithFS(
	base string, store MemoryStore, clk Clock, sink CandidateEventSink, sys fileSystem,
) *FileCandidateLedger {
	if sink == nil {
		sink = discardCandidateEvents{}
	}
	return &FileCandidateLedger{base: base, store: store, clock: clk, fs: sys, sink: sink}
}

// candidatePath returns the on-disk path of a candidate. Kind and name are
// validated before this is called, so neither can contain a separator and
// the result is always inside base.
func (l *FileCandidateLedger) candidatePath(kind MemoryKind, name string) string {
	return filepath.Join(l.base, candidatesDir, string(kind), name+candidateSuffix)
}

// load reads a candidate. found is false only when no file is there; an
// unreadable file is an error, never a silent absence, because counting
// unreadable evidence as no evidence would restart a count that had
// already been earned and would let a promoted candidate promote twice.
func (l *FileCandidateLedger) load(kind MemoryKind, name string) (candidateRecord, bool, error) {
	if err := checkKey(kind, name); err != nil {
		return candidateRecord{}, false, err
	}
	data, err := l.fs.ReadFile(l.candidatePath(kind, name))
	if err != nil {
		if isNotExist(err) {
			return candidateRecord{}, false, nil
		}
		return candidateRecord{}, false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading memory candidate %s/%s: %v", kind, name, err)
	}
	rec, err := decodeCandidate(data)
	if err != nil {
		return candidateRecord{}, false, err
	}
	return rec, true, nil
}

// persist writes a candidate atomically, so an interruption leaves the
// previous counts intact rather than a truncated record.
func (l *FileCandidateLedger) persist(r candidateRecord) error {
	data, err := encodeCandidate(r.canonical())
	if err != nil {
		return err
	}
	if err := l.fs.WriteAtomic(l.candidatePath(MemoryKind(r.Kind), r.Name), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing memory candidate %s/%s: %v", r.Kind, r.Name, err)
	}
	return nil
}

// checkObservation refuses an observation the ledger must not count.
//
// The draft is validated here, at the moment it is recorded, rather than
// at promotion: promotion runs mechanically with no caller present, so
// evidence that could never become a valid durable record must not be
// allowed to accumulate toward opening that door in the first place.
func (l *FileCandidateLedger) checkObservation(obs Observation, now time.Time) error {
	if err := validateSessionID(obs.SessionID); err != nil {
		return err
	}
	if err := checkKey(obs.Draft.Kind, obs.Draft.Name); err != nil {
		return err
	}
	return obs.Draft.Validate(now)
}

// Observe records one reference to a candidate.
func (l *FileCandidateLedger) Observe(ctx context.Context, obs Observation) (CandidateEntry, error) {
	if err := ctx.Err(); err != nil {
		return CandidateEntry{}, cascade.Wrap(cascade.KindCanceled, err, "candidate observe canceled")
	}
	now := l.clock.Now().UTC()
	if err := l.checkObservation(obs, now); err != nil {
		return CandidateEntry{}, err
	}
	rec, found, err := l.load(obs.Draft.Kind, obs.Draft.Name)
	if err != nil {
		return CandidateEntry{}, err
	}
	switch {
	case !found:
		rec = newCandidateRecord(obs, now)
	case rec.Status == string(CandidatePromoted):
		// R-14.22: observing a promoted candidate changes nothing, writes
		// nothing, and emits nothing.
		return rec.view(), nil
	default:
		rec = applyObservation(rec, obs, now)
	}
	if err := l.persist(rec); err != nil {
		return CandidateEntry{}, err
	}
	return rec.canonical().view(), nil
}

// newCandidateRecord starts a candidate at its first observation.
func newCandidateRecord(obs Observation, now time.Time) candidateRecord {
	return candidateRecord{
		Format:     candidateFormatVersion,
		Name:       obs.Draft.Name,
		Kind:       string(obs.Draft.Kind),
		Status:     string(CandidatePending),
		RefCount:   1,
		SessionIDs: []string{obs.SessionID},
		FirstSeen:  now,
		UpdatedAt:  now,
		Draft:      draftOf(obs.Draft),
	}
}

// applyObservation folds one observation into an existing pending or
// reverted candidate.
//
// A repeat from a session already counted refreshes the draft and the
// timestamp but does not move the count: one session saying the same thing
// many times is one piece of evidence, and letting it count many times is
// the straightforward way a chatty caller would walk a false belief into
// durable memory. A reverted candidate restarts from a single reference
// with only the observing session, per R-14.22; its revert history stays
// on the record so the earlier promotion remains accountable.
func applyObservation(rec candidateRecord, obs Observation, now time.Time) candidateRecord {
	if rec.Status == string(CandidateReverted) {
		rec.Status = string(CandidatePending)
		rec.SessionIDs = []string{obs.SessionID}
		rec.RefCount = 1
		rec.PromotedAt = nil
	} else if !containsSession(rec.SessionIDs, obs.SessionID) {
		rec.SessionIDs = append(rec.SessionIDs, obs.SessionID)
		rec.RefCount++
	}
	rec.Draft = draftOf(obs.Draft)
	rec.UpdatedAt = now
	return rec
}

// containsSession reports whether id is already counted. It walks a sorted
// slice rather than consulting a map so nothing about the result, or the
// order anything derived from it is written in, depends on map iteration.
func containsSession(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}

// Promote writes the candidate's draft as a durable record and marks the
// candidate promoted.
//
// The durable write happens first. If it fails, the candidate keeps its
// pending status and can be promoted again; the reverse order would leave
// a candidate claiming a record that was never written.
func (l *FileCandidateLedger) Promote(
	ctx context.Context, kind MemoryKind, name string,
) (CandidateEntry, error) {
	if err := ctx.Err(); err != nil {
		return CandidateEntry{}, cascade.Wrap(cascade.KindCanceled, err, "candidate promote canceled")
	}
	now := l.clock.Now().UTC()
	rec, err := l.mustLoad(kind, name)
	if err != nil {
		return CandidateEntry{}, err
	}
	if rec.Status == string(CandidatePromoted) {
		return CandidateEntry{}, cascade.Wrapf(cascade.KindConflict, ErrAlreadyPromoted,
			"memory candidate %s/%s is already promoted", kind, name)
	}
	if err := l.store.Write(ctx, rec.entry()); err != nil {
		return CandidateEntry{}, err
	}
	rec.Status = string(CandidatePromoted)
	rec.PromotedAt = &now
	rec.UpdatedAt = now
	if err := l.persist(rec); err != nil {
		return CandidateEntry{}, err
	}
	view := rec.canonical().view()
	return view, l.sink.CandidatePromoted(ctx, promotionEventOf(view, now))
}

// Revert takes a promotion back.
func (l *FileCandidateLedger) Revert(
	ctx context.Context, kind MemoryKind, name, reason string,
) (CandidateEntry, error) {
	if err := ctx.Err(); err != nil {
		return CandidateEntry{}, cascade.Wrap(cascade.KindCanceled, err, "candidate revert canceled")
	}
	now := l.clock.Now().UTC()
	rec, err := l.mustLoad(kind, name)
	if err != nil {
		return CandidateEntry{}, err
	}
	if rec.Status != string(CandidatePromoted) {
		return CandidateEntry{}, cascade.Wrapf(cascade.KindConflict, ErrNotPromoted,
			"memory candidate %s/%s is %s, not promoted", kind, name, rec.Status)
	}
	rec.Status = string(CandidateReverted)
	rec.RevertedAt = &now
	rec.RevertReason = reason
	rec.PromotedAt = nil
	rec.UpdatedAt = now
	if err := l.persist(rec); err != nil {
		return CandidateEntry{}, err
	}
	view := rec.canonical().view()
	return view, l.sink.CandidateReverted(ctx, RevertEvent{
		Name: view.Name, Kind: view.Kind, Reason: reason, RevertedAt: now,
	})
}

// mustLoad reads a candidate that has to exist.
func (l *FileCandidateLedger) mustLoad(kind MemoryKind, name string) (candidateRecord, error) {
	rec, found, err := l.load(kind, name)
	if err != nil {
		return candidateRecord{}, err
	}
	if !found {
		return candidateRecord{}, cascade.Wrapf(cascade.KindNotFound, ErrNoSuchCandidate,
			"no memory candidate %s/%s", kind, name)
	}
	return rec, nil
}
