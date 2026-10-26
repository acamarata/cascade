package conversation

// Purpose: a thread's privacy mode — the §5.16 sensitivity tier that
//   governs where its content may be routed.
// Inputs: a threadID and a provider.SensitivityTier.
// Outputs: the tier recorded in a marker table, read back fail-closed.
// Constraints: ABSENCE MEANS RESTRICTED. A thread with no row is not
//   "unknown" and is never "public" — it is restricted, which is
//   provider.SensitivityTier's own zero value and §5.16's fail-closed
//   rule. That makes the default true by construction rather than by a
//   branch someone could invert.
//
//   A NEW TABLE, NOT A COLUMN, for the reason archive.go states at
//   length: the migrate DSL offers no ALTER step at all, and dsl.go's own
//   doc comment prescribes exactly this shape — "adding a column ... means
//   authoring a new MigrationStep for a new table".
//
// SPORT: internal.conversation.privacy/ADDED (P1-E20-W5-S44-T2).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ThreadPrivacyStore is the privacy_mode half of Store, declared here so
// the surface and the implementation that satisfies it read together.
type ThreadPrivacyStore interface {
	// SetThreadPrivacy records threadID's §5.16 sensitivity tier,
	// WRITE-ONCE: the first write wins; a later write naming the same tier
	// is a no-op and one naming a different tier is refused with
	// KindConflict, the marker unchanged. A thread that already has turns
	// and no marker is refused too: its implicit restricted tier is never
	// rewritten after content exists (setThreadPrivacyTx).
	SetThreadPrivacy(ctx context.Context, threadID string, tier provider.SensitivityTier) error
	// CreateThread inserts a brand-new thread row (zero turns), its §5.16
	// marker and its creating caller's attribution (scope_attr.go) in ONE
	// transaction (chat.create_thread). An existing id is KindConflict with
	// nothing written; any failed insert rolls back all three. createdAt is
	// a caller-resolved Clock reading. The thread_scope schema must already
	// be applied (ensureScopeSchema); without it the attribution insert
	// fails and nothing is written.
	CreateThread(ctx context.Context, threadID string, tier provider.SensitivityTier, attribution string, createdAt int64) error
	// ThreadPrivacy reads threadID's tier. A thread with no row -- and a
	// thread that does not exist -- reads as restricted, never as an
	// error the caller could mistake for permission.
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
	// AppendFirstTurn creates turn.ThreadID with tier as its marker and
	// commits its first turn and segments, all in ONE transaction
	// (chat.append_turn with no thread_id): a refused or failed write
	// leaves no thread, no turn and no marker.
	AppendFirstTurn(ctx context.Context, tier provider.SensitivityTier, turn Turn, segments []Segment) error
}

// tableThreadPrivacy is the privacy-mode marker table: one row per thread
// whose mode was set explicitly, absent for every thread running on the
// fail-closed default.
const tableThreadPrivacy = "conversation_thread_privacy"

// privacyStep is MigrationSet's step for the marker table. A function
// rather than a literal inline in domain.go, which sits on Art.10.3's
// 300-line cap.
func privacyStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "conversation_thread_privacy: per-thread §5.16 sensitivity tier",
		Table: &migrate.TableDef{
			Name:    tableThreadPrivacy,
			Columns: []migrate.ColumnDef{id("thread_id"), text("privacy_mode")},
			// NO foreign key to the thread table, unlike the archive
			// marker beside it. Archival marks a thread that already
			// exists; a privacy mode is recorded on the request that
			// CREATES one, before its first turn is committed, because an
			// unmarked thread reads as restricted and any window between
			// "has content" and "is marked local-only" is a window in
			// which it routes under the looser tier. An FK makes that
			// ordering fail outright the moment PRAGMA foreign_keys is
			// ON — it was declared here and only worked because SQLite
			// leaves FK enforcement off by default, which is a latent
			// break, not a working design. It also contradicts this
			// table's own contract: ThreadPrivacy answers restricted for
			// a thread that does not exist, so a row for one is defined
			// as harmless rather than as an integrity error.
		},
	}
}

// SetThreadPrivacy implements Store. It does NOT require the thread to
// exist: the mode is recorded on the request that CREATES a thread, before
// its first turn commits, because an unmarked thread reads as restricted
// and any window between "has content" and "is marked local-only" would
// route under the looser tier. A mark for an id that never gets a turn is
// inert; ThreadPrivacy already answers restricted for an id it cannot find.
//
// WRITE-ONCE, in one transaction (setThreadPrivacyTx,
// privacy_createthread.go): a marker is never widened or narrowed after it
// is written, and a thread that already holds turns without a marker keeps
// its implicit restricted tier.
func (s *conversationStore) SetThreadPrivacy(ctx context.Context, threadID string, tier provider.SensitivityTier) error {
	if threadID == "" {
		return ErrInvalidRecord
	}
	if !tier.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"conversation: %q is not a sensitivity tier", tier.String())
	}
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		return setThreadPrivacyTx(ctx, tx, threadID, tier)
	})
}

// setThreadPrivacyTx, CreateThread and AppendFirstTurn live in
// privacy_createthread.go, split under Art.10.3's 300-line cap.

// ThreadPrivacy implements Store.
//
// A thread with no row, and a thread that does not exist at all, both read
// as restricted. That is deliberate: a caller asking where it may route
// content for an id it cannot find must not be told "public", and a
// missing thread is not a routing permission. The refusal a caller needs
// for a bad id comes from the operation it is actually attempting.
//
// A stored row reads through provider.ParseSensitivityTier, the one closed
// parser. A row whose text is not a tier name reads as LOCAL-ONLY, the
// narrowest tier: a corrupt or hand-edited value is exactly the case where
// guessing wide would be worst, and restricted still permits external
// lanes. An empty stored value is the documented zero value, restricted.
func (s *conversationStore) ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT privacy_mode FROM `+tableThreadPrivacy+` WHERE thread_id = ?`, threadID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return provider.SensitivityRestricted, nil
	}
	if err != nil {
		return provider.SensitivityRestricted,
			cascade.Wrap(cascade.KindUnavailable, err, "conversation: read thread privacy mode")
	}
	// The parse error is deliberately not returned: the tier it comes with
	// is local-only, and a corrupt row has no caller left to refuse, so the
	// narrowest routing answer is the useful one.
	tier, _ := provider.ParseSensitivityTier(name)
	return tier, nil
}

// ParseSensitivityTier maps a tier name to its tier through the closed
// provider.ParseSensitivityTier (the one name table). The empty name is the
// restricted zero value; any name that does not parse is local-only, the
// narrowest tier, so a caller that cannot refuse never widens.
//
// It is NOT the stored-row reader: ThreadPrivacy parses rows itself.
// firstTurnTier calls this only after the closed parser has accepted the
// name.
func ParseSensitivityTier(name string) provider.SensitivityTier {
	tier, err := provider.ParseSensitivityTier(name)
	if err != nil {
		return provider.SensitivityLocalOnly
	}
	return tier
}

// SetThreadPrivacy exposes Store.SetThreadPrivacy through the adapter.
func (a *Adapter) SetThreadPrivacy(ctx context.Context, threadID string, tier provider.SensitivityTier) error {
	return a.store.SetThreadPrivacy(ctx, threadID, tier)
}

// ThreadPrivacy exposes Store.ThreadPrivacy through the adapter.
func (a *Adapter) ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error) {
	return a.store.ThreadPrivacy(ctx, threadID)
}

// resolveAppendThread (the thread chat.append_turn writes to) and the
// first-turn commit live in adapter_appendtarget.go.

// AppendFirstTurn implements Store: a brand-new thread's row, its marker,
// its first turn (with its origin stamp) and that turn's segments, in one
// transaction. Any failure (a colliding id, a segment the store refuses)
// rolls back the marker and the thread row too.
func (s *conversationStore) AppendFirstTurn(ctx context.Context, tier provider.SensitivityTier, turn Turn, segments []Segment) error {
	if err := validateFirstTurn(tier, turn, segments); err != nil {
		return err
	}
	fts := s.hasFTS(ctx) // probed before the tx opens: see AppendSegment
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := insertNewThreadTx(ctx, tx, turn.ThreadID, turn.CreatedAt); err != nil {
			return err
		}
		if err := setThreadPrivacyTx(ctx, tx, turn.ThreadID, tier); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+tableTurn+` (id, thread_id, seq, role, created_at) VALUES (?, ?, ?, ?, ?)`,
			turn.ID, turn.ThreadID, turn.Seq, string(turn.Role), turn.CreatedAt); err != nil {
			return translateAppendError(err, "conversation: append turn")
		}
		if err := insertTurnOrigin(ctx, tx, turn); err != nil {
			return err
		}
		for _, seg := range segments {
			if err := insertSegmentTx(ctx, tx, seg, turn.ThreadID, fts); err != nil {
				return err
			}
		}
		return nil
	})
}

// validateFirstTurn checks AppendFirstTurn's inputs before any write: a
// valid tier, a structurally valid turn at Seq 0, and segments that belong
// to it, numbered 0..n-1.
func validateFirstTurn(tier provider.SensitivityTier, turn Turn, segments []Segment) error {
	if !tier.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "conversation: %q is not a sensitivity tier", tier.String())
	}
	if err := validateTurn(turn); err != nil {
		return err
	}
	if turn.Seq != 0 {
		return ErrOutOfOrder
	}
	for i, seg := range segments {
		if err := validateSegment(seg); err != nil {
			return err
		}
		if seg.TurnID != turn.ID {
			return ErrInvalidRecord
		}
		if seg.Seq != int64(i) {
			return ErrOutOfOrder
		}
	}
	return nil
}

// insertSegmentTx inserts one segment inside tx and, when the search index
// exists, mirrors it there in the same transaction (AppendSegment's rule).
func insertSegmentTx(ctx context.Context, tx *sql.Tx, seg Segment, threadID string, fts bool) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableSegment+` (id, turn_id, seq, kind, content, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		seg.ID, seg.TurnID, seg.Seq, string(seg.Kind), seg.Content, seg.CreatedAt)
	if err := translateAppendError(err, "conversation: append segment"); err != nil {
		return err
	}
	if fts {
		return mirrorSegmentFTS(ctx, tx, seg, threadID)
	}
	return nil
}
