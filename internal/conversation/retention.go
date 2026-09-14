package conversation

// Purpose: windowed retention over conversation_turn: a logical tombstone
//   (never a physical delete) for turns satisfying both an age bound and
//   a per-thread count bound (P1-E20-W5-S44-T3).
// Inputs: a RetentionPolicy plus a caller-resolved `now` (injected Clock
//   reading -- this file reads no clock itself, matching store.go's own
//   AppendTurn/AppendSegment convention of taking an already-resolved
//   timestamp rather than a Clock parameter).
// Outputs: a PruneResult naming exactly how many turns were newly
//   tombstoned this call. Physical space reclaim is the B/S-03.T2 VACUUM
//   job's job, not this file's -- PruneTurns issues logical tombstones
//   only, via a NEW conversation_turn_tombstone marker table (same
//   new-table-not-ALTER-TABLE shape as archive.go's
//   conversation_thread_archive, for the identical migrate-DSL reason).
// Constraints: idempotent by construction -- the tombstone marker's own
//   PRIMARY KEY plus ON CONFLICT DO NOTHING means a second PruneTurns
//   call against unchanged data finds every prior candidate already
//   excluded by the LEFT JOIN below and tombstones zero additional rows.
//   CONTRACT-VS-TREE (journal, quoted in full there): this ticket's text
//   calls for "turns_max count per thread CLASS", but no Thread field
//   anywhere in this tree carries a class/category -- domain.go's Thread
//   is {ID, Name, CreatedAt} only, and no other ticket has introduced
//   one. This implementation scopes turns_max PER THREAD (the closest
//   sound reading absent a class concept), not per class.
// SPORT: internal.conversation.retention/ADDED (P1-E20-W5-S44-T3).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// tableTurnTombstone is the logical-prune marker table: one row per
// tombstoned turn id.
const tableTurnTombstone = "conversation_turn_tombstone"

// RetentionPolicy bounds PruneTurns. Either field <=0 disables pruning
// entirely (both dimensions must hold for a turn to qualify, so a
// zero-window policy is a documented no-op -- this ticket's own
// "prune zero-window no-op" test case).
type RetentionPolicy struct {
	// AgeMaxDays: a turn must be older than this many days to qualify.
	AgeMaxDays int
	// TurnsMaxPerThread: a turn must rank beyond this many most-recent
	// turns within its OWN thread to qualify (see this file's Purpose
	// doc comment on the "per thread class" contract-vs-tree gap).
	TurnsMaxPerThread int
}

// PruneResult reports one PruneTurns call's outcome.
type PruneResult struct {
	// Tombstoned counts turns newly tombstoned by this call (not the
	// total ever tombstoned) -- a repeat call against unchanged data
	// reports 0, the idempotency AC.
	Tombstoned int
}

// PruneTurns implements Store.
func (s *conversationStore) PruneTurns(ctx context.Context, policy RetentionPolicy, now int64) (PruneResult, error) {
	if policy.AgeMaxDays <= 0 || policy.TurnsMaxPerThread <= 0 {
		return PruneResult{}, nil
	}
	cutoff := now - int64(policy.AgeMaxDays)*86400
	ids, err := s.findPruneCandidates(ctx, cutoff, policy.TurnsMaxPerThread)
	if err != nil {
		return PruneResult{}, err
	}
	n, err := s.tombstoneTurns(ctx, ids, now)
	if err != nil {
		return PruneResult{}, err
	}
	return PruneResult{Tombstoned: n}, nil
}

// findPruneCandidates returns the ids of turns older than cutoff AND
// ranked beyond maxPerThread most-recent (by Seq) within their own
// thread, excluding turns already tombstoned. MUTATION-TESTED FINDING
// (journal, quoted in full there): idempotency's actual guarantee is
// tombstoneTurns's own ON CONFLICT DO NOTHING below, NOT this exclusion
// -- removing the "ts.turn_id IS NULL" clause here still leaves
// TestPruneTurns_ExactRowCountAndIdempotentDoublePrune green, since a
// re-selected already-tombstoned id simply inserts 0 rows on conflict.
// This WHERE clause remains as a real optimization (never re-scanning
// rows PruneTurns will insert 0 for), documented accurately rather than
// left claiming a correctness role it does not have.
func (s *conversationStore) findPruneCandidates(ctx context.Context, cutoff int64, maxPerThread int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id FROM `+tableTurn+` t
		LEFT JOIN `+tableTurnTombstone+` ts ON ts.turn_id = t.id
		WHERE ts.turn_id IS NULL
		  AND t.created_at < ?
		  AND (SELECT COUNT(*) FROM `+tableTurn+` newer
		       WHERE newer.thread_id = t.thread_id AND newer.seq > t.seq) >= ?`,
		cutoff, maxPerThread)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: find prune candidates")
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: scan prune candidate")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: iterate prune candidates")
	}
	return ids, nil
}

// tombstoneTurns inserts one marker row per id, at tombstonedAt, and
// returns how many rows were newly inserted (ON CONFLICT DO NOTHING means
// an id already tombstoned by a prior call contributes 0, not 1).
func (s *conversationStore) tombstoneTurns(ctx context.Context, ids []string, tombstonedAt int64) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO `+tableTurnTombstone+` (turn_id, tombstoned_at) VALUES (?, ?) ON CONFLICT(turn_id) DO NOTHING`,
			id, tombstonedAt)
		if err != nil {
			return n, cascade.Wrap(cascade.KindUnavailable, err, "conversation: tombstone turn")
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return n, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read tombstone rows affected")
		}
		n += int(affected)
	}
	return n, nil
}

// PruneTurns exposes Store.PruneTurns through the adapter surface as a
// manually-triggerable maintenance operation (see pagination.go's doc
// comment on the adapter's other new, not-yet-RPC-registered operations).
// FINDING (journal, quoted in full there): this ticket's own AC calls for
// PruneTurns to "register as a valid C/S-04.T4 scheduler entry point",
// but C/S-04.T4 (internal/events/scheduler) is already CLOSED and
// registered only B/S-03.T2's storage-level retention jobs -- no ticket
// in the current corpus owns wiring a conversation-domain runnable onto
// the live daemon scheduler. cursor_fuzz_test.go's sibling,
// retention_test.go's TestPruneTurns_SchedulerEntryPointSignature, proves
// PruneTurns's shape is accepted by a REAL scheduler.Scheduler
// (RegisterRunnable), satisfying the AC's own "standalone invocation
// test with a test scheduler" wording without inventing a wiring ticket
// that does not exist.
func (a *Adapter) PruneTurns(ctx context.Context, policy RetentionPolicy) (PruneResult, error) {
	return a.store.PruneTurns(ctx, policy, a.clock.Now().Unix())
}
