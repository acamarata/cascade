package journal

// Purpose: Replay — a streaming forward scan of one entity's log from a
//
//	cursor, with the checkpoint-loss fallback, the kinds filter, and
//	idempotent-by-operation_id deduplication (R-21.216).
//
// Inputs: an entityID, a caller-supplied Cursor, and an optional kinds
//
//	filter (empty means all eight).
//
// Outputs: entries in strictly increasing Seq order, or a pkg/cascade
//
//	taxonomy error.
//
// Constraints: never blocks for entries appended after the call starts
//
//	(that is Subscribe-shaped behavior this package does not offer); never
//	drops an entry in range (at-least-once delivery — a re-delivered
//	entry is possible via the fallback path, a dropped one is not).
//
// SPORT: internal.fleet.journal.Store/CHANGED (Replay) (P1-E13-W3-S27-T1).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Replay returns entityID's entries with Seq greater than the resolved
// start position, filtered by kinds (empty means all kinds) and
// deduplicated by operation_id so a retried Append is applied at most
// once by a caller that walks the returned entries in order.
//
// The start position is cursor.Seq when cursor.EntityID equals entityID.
// Any other cursor — including the zero value — is treated as absent or
// stale, and Replay falls back to the last sequence entityID's own
// persisted Checkpoint covers (0 if none has ever been published). The
// fallback is always safe: it may re-deliver entries a caller already
// processed, but it never skips one.
func (s *SQLiteStore) Replay(ctx context.Context, entityID string, cursor Cursor, kinds []Kind) ([]Entry, error) {
	if entityID == "" {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "replay requires an entity id")
	}
	lock := s.lockFor(entityID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.recoverEntityLocked(ctx, entityID); err != nil {
		return nil, err
	}
	startSeq, err := s.resolveStartSeq(ctx, entityID, cursor)
	if err != nil {
		return nil, err
	}
	entries, err := s.scanEntityFrom(ctx, entityID, startSeq)
	if err != nil {
		return nil, err
	}
	return dedupeByOperationID(filterKinds(entries, kinds)), nil
}

// resolveStartSeq implements the cursor-loss fallback described on
// Replay's doc comment above.
func (s *SQLiteStore) resolveStartSeq(ctx context.Context, entityID string, cursor Cursor) (uint64, error) {
	if cursor.EntityID == entityID {
		return cursor.Seq, nil
	}
	rec, ok, err := s.loadCheckpointFrom(ctx, s.store, entityID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return rec.Seq, nil
}

// scanEntityFrom reads every entry for entityID with Seq in
// (startSeq, head], where head is entityID's current (post-recovery)
// head pointer. A gap left by recovery's torn-tail truncation reads as
// "no more entries after this point" rather than an error, since recovery
// only ever removes a log's trailing suffix.
func (s *SQLiteStore) scanEntityFrom(ctx context.Context, entityID string, startSeq uint64) ([]Entry, error) {
	head, err := s.loadHeadFrom(ctx, s.store, entityID)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for seq := startSeq + 1; seq <= head; seq++ {
		data, gerr := s.store.Get(ctx, s.namespace, entryKey(entityID, seq))
		if gerr != nil {
			if cascade.HasKind(gerr, cascade.KindNotFound) {
				continue
			}
			return nil, wrapStore(gerr, "journal: replay scan")
		}
		e, derr := decodeEntry(data)
		if derr != nil {
			return nil, derr
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// filterKinds returns only the entries whose Kind is in kinds. An empty
// kinds slice means "all kinds" and returns entries unchanged.
func filterKinds(entries []Entry, kinds []Kind) []Entry {
	if len(kinds) == 0 {
		return entries
	}
	allowed := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		allowed[k] = true
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if allowed[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}

// dedupKey identifies one (kind, operation_id) pair for dedupeByOperationID.
// The key is Kind-scoped, not OperationID alone, because an Intent and its
// Ack legitimately share one operation_id (R-21.216's intent/acknowledgement
// protocol) and are two distinct lifecycle records, not a repeated entry.
type dedupKey struct {
	kind Kind
	op   string
}

// dedupeByOperationID keeps only the first (lowest Seq) entry seen for each
// (kind, operation_id) pair. This is Replay's idempotency guarantee: if a
// caller retried an Append whose earlier attempt had already committed
// (because the caller could not tell whether the first attempt's commit
// had happened), the log now holds two entries of the same kind naming the
// same operation_id, and only the first is handed to a consumer that
// applies entries in the order Replay returns them — so replaying an
// already-applied operation is a no-op, not a second application.
func dedupeByOperationID(entries []Entry) []Entry {
	seen := make(map[dedupKey]bool, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		key := dedupKey{kind: e.Kind, op: e.OperationID}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
