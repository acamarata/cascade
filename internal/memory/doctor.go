package memory

// Purpose: the projection's per-record scrub and its orphan check — the
//   two things a forget pipeline needs from the derived read side, and the
//   doctor check that proves a forget actually landed. A record removed
//   from the files but still answering from the index is the memory
//   system's version of an orphaned blob, and this file is where it is
//   found and named.
// Inputs: an already-built ProjectionJob (its files, its key-value store
//   and its optional vector index).
// Outputs: an IndexTrace counting what was actually removed, a list of
//   orphaned rows, or a pkg/cascade taxonomy error.
// Constraints: THE FILES ARE STILL THE SOURCE OF TRUTH. Nothing here
//   writes a file, and nothing here decides that a record should go: it
//   removes the derived traces of a decision already taken against the
//   files. No clock is read; no map iteration reaches an ordered result.
// SPORT: internal/memory/doctor (ADD, P1-E07-W2-S14-T4).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// IndexTrace counts what one record actually had in the projection.
//
// It is a count of what was REMOVED, not an assertion that a removal was
// attempted. That distinction is the whole point of returning it: a forget
// that reports "index scrubbed" without knowing whether there was anything
// there claims a guarantee it did not check, and this type is what lets
// the claim be checked instead.
type IndexTrace struct {
	// ID is the canonical "<kind>/<name>" address the trace speaks for.
	ID string `json:"id"`
	// Row reports that a projected row existed and was removed. It is true
	// for a row that could not be decoded as well as for one that could:
	// an undecodable row is still a row that was answering for this
	// address, and it is still gone afterwards.
	Row bool `json:"row"`
	// Postings is how many full-text posting keys were retracted. It is
	// the stored row's own token set, which is exactly what the projection
	// wrote, so no posting is guessed at and none is missed. It is zero
	// for a row that could not be decoded, because such a row's token set
	// is not readable; RowUnreadable says so rather than letting the zero
	// be read as "there were none".
	Postings int `json:"postings"`
	// RowUnreadable reports that a row existed but could not be decoded,
	// so Postings is unknown rather than zero.
	RowUnreadable bool `json:"row_unreadable,omitempty"`
	// Vector reports that a vector existed for this address and is gone.
	// It is measured as a drop in the vector index's own count across the
	// delete, because the VectorStore contract has no get-by-id: inferring
	// presence from the row would report a vector that was never written.
	Vector bool `json:"vector"`
	// VectorProbed reports whether the vector leg was configured at all.
	// Without it, Vector is false because there was no index, not because
	// the index was checked and found clean.
	VectorProbed bool `json:"vector_probed"`
}

// Empty reports that this trace removed nothing. A second scrub of the
// same address returning an empty trace is the verification that the first
// one finished: it asserts the absence directly rather than trusting the
// first call's return code.
func (t IndexTrace) Empty() bool { return !t.Row && t.Postings == 0 && !t.Vector }

// ScrubRecord removes every trace of one record from the projection: its
// row, the postings that row wrote, and its vector.
//
// It is the interface the forget pipeline calls, and it is idempotent:
// scrubbing an address the projection does not hold removes nothing,
// returns an empty IndexTrace and no error. That is what makes a scrub
// safe to re-run after an interruption, and what makes a second call a
// usable proof that the first one left nothing behind.
//
// It does NOT consult the files. A caller asks for this because it has
// already decided, against the files, that the record is gone; a scrub
// that second-guessed that decision would make the projection an authority
// over the tree, which it is not.
func (j *ProjectionJob) ScrubRecord(ctx context.Context, id string) (IndexTrace, error) {
	trace := IndexTrace{ID: id}
	row, found, err := readRow(ctx, j.kv, id)
	switch {
	case err != nil && cascade.HasKind(err, cascade.KindIntegrity):
		trace.Row, trace.RowUnreadable = true, true
	case err != nil:
		return trace, err
	case found:
		trace.Row, trace.Postings = true, len(row.Tokens)
	}
	before, probed, err := j.vectorCount(ctx)
	if err != nil {
		return trace, err
	}
	trace.VectorProbed = probed
	if werr := j.withdraw(ctx, id); werr != nil {
		return trace, werr
	}
	if probed {
		after, _, aerr := j.vectorCount(ctx)
		if aerr != nil {
			return trace, aerr
		}
		trace.Vector = after < before
	}
	return trace, nil
}

// vectorCount returns how many vectors the projection namespace holds, and
// whether the vector leg is configured at all. A job with no vector leg
// reports probed=false rather than a count of zero, so "no vectors" and
// "no vector index" are never confused for each other.
func (j *ProjectionJob) vectorCount(ctx context.Context) (int, bool, error) {
	if j.vectors == nil {
		return 0, false, nil
	}
	n, err := j.vectors.Count(ctx, projectionNamespace)
	if err != nil {
		return 0, true, wrapKV(err, "counting projected vectors")
	}
	return n, true, nil
}

// Orphans returns every projected row that is still answering queries for
// a record the files no longer hold, in canonical address order.
//
// This is the doctor check. It is the memory store's equivalent of the
// orphaned-blob check: a row whose record has been tombstoned or removed,
// and which has not been retired, is a search hit for something a user has
// already been told is gone. Finding one means a forget was interrupted
// between removing the file and scrubbing the index, or that the
// projection has not run since a record was deleted outside the store.
//
// A row already marked Deleted is NOT an orphan. The projection keeps such
// rows deliberately, with their body and postings cleared, so a name that
// comes back flips the same row live again; it answers no query.
func (j *ProjectionJob) Orphans(ctx context.Context) ([]IndexTrace, error) {
	keys, err := scanKeys(ctx, j.kv, recordPrefix)
	if err != nil {
		return nil, wrapKV(err, "scanning projected rows")
	}
	out := make([]IndexTrace, 0)
	for _, key := range keys {
		id := key[len(recordPrefix):]
		trace, orphaned, oerr := j.orphanOf(ctx, id)
		if oerr != nil {
			return nil, oerr
		}
		if orphaned {
			out = append(out, trace)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// orphanOf judges one projected row against the files. Split from Orphans
// to keep both inside the 50-line function cap.
//
// An address the row key carries but that is not a legal canonical address
// is reported as an orphan too: nothing the projection is allowed to write
// produces such a key, so one that exists cannot be matched to a file and
// cannot be judged live.
func (j *ProjectionJob) orphanOf(ctx context.Context, id string) (IndexTrace, bool, error) {
	trace := IndexTrace{ID: id, Row: true}
	kind, name, err := ParseAddress(id)
	if err != nil {
		trace.RowUnreadable = true
		return trace, true, nil
	}
	row, found, rerr := readRow(ctx, j.kv, id)
	switch {
	case rerr != nil && cascade.HasKind(rerr, cascade.KindIntegrity):
		trace.RowUnreadable = true
		return trace, true, nil
	case rerr != nil:
		return trace, false, rerr
	case !found:
		return trace, false, nil
	case row.Deleted:
		return trace, false, nil
	}
	trace.Postings = len(row.Tokens)
	live, lerr := j.files.Exists(ctx, kind, name)
	if lerr != nil {
		return trace, false, lerr
	}
	return trace, !live, nil
}
