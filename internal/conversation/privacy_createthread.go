package conversation

// Purpose (this file): the store-layer writes that give a thread its
//   permanent §5.16 marker: setThreadPrivacyTx (the write-once
//   insert-or-refuse SetThreadPrivacy and CreateThread share) and
//   CreateThread (chat.create_thread: thread row + marker + attribution).
//   Split from privacy.go under the 300-line cap.
// Inputs: a threadID, a tier, the creating caller's attribution scope and a
//   caller-resolved createdAt.
// Outputs: nil, or a typed refusal: errThreadExists (KindConflict) for an
//   id already in use, KindConflict when a marker already carries a
//   different tier or a thread already holds turns without one,
//   KindInvalidInput for a malformed request, KindUnavailable for a store
//   failure.
// Constraints: ONE TRANSACTION per call. The thread row, its marker and its
//   attribution commit together or not at all, so no reader ever sees a
//   minted thread without its marker and a refused write leaves nothing
//   behind. Markers are WRITE-ONCE: an equal tier is a no-op, a different
//   one is KindConflict and nothing changes.
// SPORT: internal.conversation.privacy (create_thread, write-once markers).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// errThreadExists refuses a CreateThread whose id is already a thread:
// creation never adopts an existing thread or its history.
var errThreadExists = cascade.New(cascade.KindConflict,
	"conversation: create thread: the thread id already exists")

// errUnmarkedThreadHasTurns refuses a marker for a thread that already
// holds turns without one. Its implicit tier is restricted, and content
// written under that tier is never re-labelled afterwards.
var errUnmarkedThreadHasTurns = cascade.New(cascade.KindConflict,
	"conversation: thread already has turns and no privacy mode; its restricted tier cannot be changed")

// setThreadPrivacyTx writes threadID's marker inside tx, write-once. The
// INSERT ... DO NOTHING runs first, so the transaction takes its write lock
// before it reads anything back; the read-back then decides:
//   - inserted, and the thread holds no turns: the marker is new; done.
//   - inserted, but the thread already holds turns: refused (the caller's
//     transaction rolls the insert back), never a late re-label.
//   - not inserted: an earlier write won; the same tier is a no-op and a
//     different one is KindConflict with the stored marker untouched.
func setThreadPrivacyTx(ctx context.Context, tx *sql.Tx, threadID string, tier provider.SensitivityTier) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableThreadPrivacy+` (thread_id, privacy_mode) VALUES (?, ?) `+
			`ON CONFLICT(thread_id) DO NOTHING`,
		threadID, tier.String())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: set thread privacy mode")
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: set thread privacy mode")
	}
	if inserted == 1 {
		return refuseIfTurnsExist(ctx, tx, threadID)
	}
	var stored string
	if err := tx.QueryRowContext(ctx,
		`SELECT privacy_mode FROM `+tableThreadPrivacy+` WHERE thread_id = ?`, threadID,
	).Scan(&stored); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: read back thread privacy mode")
	}
	if stored != tier.String() {
		return errThreadPrivacyAlreadySet(threadID, stored, tier)
	}
	return nil
}

// refuseIfTurnsExist is setThreadPrivacyTx's guard for a marker it has just
// inserted: a thread that already holds a turn keeps its implicit tier.
func refuseIfTurnsExist(ctx context.Context, tx *sql.Tx, threadID string) error {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM `+tableTurn+` WHERE thread_id = ? LIMIT 1`, threadID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: check thread turns")
	}
	return errUnmarkedThreadHasTurns
}

// errThreadPrivacyAlreadySet reports a write-once conflict: threadID's
// marker already carries stored, and the call asked for want. KindConflict:
// the stored state disagrees with the request and nothing was changed.
func errThreadPrivacyAlreadySet(threadID, stored string, want provider.SensitivityTier) error {
	return cascade.Newf(cascade.KindConflict,
		"conversation: thread %q privacy mode is already %s; it cannot be changed to %s "+
			"(privacy markers are write-once)", threadID, stored, want.String())
}

// insertNewThreadTx inserts threadID's thread row inside tx, refusing an id
// that already exists (errThreadExists) instead of adopting it. The insert is
// the transaction's FIRST statement: a read before it would take a shared lock
// the write must then upgrade, and two overlapping mints deadlock on that
// upgrade (SQLITE_BUSY, which the busy timeout does not retry).
func insertNewThreadTx(ctx context.Context, tx *sql.Tx, threadID string, createdAt int64) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableThread+` (id, name, created_at) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		threadID, threadID, createdAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: create thread")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: create thread")
	}
	if n == 0 {
		return errThreadExists
	}
	return nil
}

// CreateThread implements Store: the thread row, its marker and its
// attribution in one transaction (this file's header).
func (s *conversationStore) CreateThread(ctx context.Context, threadID string, tier provider.SensitivityTier,
	attribution string, createdAt int64) error {
	if threadID == "" || attribution == "" || createdAt <= 0 {
		return ErrInvalidRecord
	}
	if !tier.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"conversation: %q is not a sensitivity tier", tier.String())
	}
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := insertNewThreadTx(ctx, tx, threadID, createdAt); err != nil {
			return err
		}
		if err := setThreadPrivacyTx(ctx, tx, threadID, tier); err != nil {
			return err
		}
		return attributeThread(tx, threadID, attribution)
	})
}
