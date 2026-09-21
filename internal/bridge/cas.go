// Purpose (this file): Save — the bridge row's COMPARE-AND-SWAP write, and
//
//	the conflict every caller of it has to be able to see.
//
// WHY THIS IS NOT AN UPSERT ANY MORE, AND WHY THAT WAS A REAL DEFECT. Every
//
//	bridge mutation is a read-modify-write over the WHOLE row: the pairing
//	code store loads the row to check a digest, the binding store loads it to
//	add a sender to the allowlist, and the update ledger loads it to advance
//	the long-poll offset. With a bare `INSERT … ON CONFLICT DO UPDATE SET`
//	every one of those wrote back all nine columns, so the LAST writer won
//	with a copy of the row as it looked when IT read — and the loser's
//	columns were silently reverted. That is not theoretical: issuance and the
//	poll loop run in one daemon process, so `pa.pair_code` saving its stale
//	copy could revert the poller's offset and replay window, and the same
//	interleaving around a Bind could revert AllowedFrom, which reads as
//	"this bot is not paired" and quietly UNPAIRS a bound bot.
//
// Inputs: a SubjectRow carrying the Version that Load handed back.
// Outputs: the row written and its version advanced by one, or
//
//	ErrVersionConflict — never a silent overwrite.
//
// Constraints: the conflict is a TYPED, IDENTIFIABLE error, not merely a
//
//	KindConflict (pkg/cascade's Is compares the KIND only, so a kind check
//	alone would also match any other conflict in the tree). Callers retry
//	their own read-modify-write once; see plugins/cascade-pa/concurrency.go.
//
// SPORT: internal.bridge.Store/CHANGED (compare-and-swap) — P1-E23-W5-S48-T1.

package bridge

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrVersionConflict reports that the row changed between the caller's Load
// and its Save, so the write was REFUSED rather than applied over somebody
// else's newer state. Exported so a caller can recognise it by identity and
// retry its own read-modify-write, and so a test can assert on it without
// matching message text.
var ErrVersionConflict = cascade.New(cascade.KindConflict,
	"bridge: the subject row changed since it was read; the write was refused")

// IsVersionConflict reports whether err is a compare-and-swap refusal from
// this package. It checks the message as well as the kind, because
// pkg/cascade's Is compares the kind only and every unrelated KindConflict
// in the tree would otherwise look like a CAS miss.
func IsVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	return cascade.HasKind(err, cascade.KindConflict) && strings.Contains(err.Error(), conflictMarker)
}

// conflictMarker is the fragment IsVersionConflict recognises. A constant so
// the sentinel's text and the predicate cannot drift apart.
const conflictMarker = "changed since it was read"

// subjectColumns is the column list both statements below write, kept in one
// place so an added column cannot reach one statement and miss the other.
const subjectColumns = `subject, trust_tier, paired_at, allowed_from, code_digest,
	code_expires_at, wrong_attempts, poll_offset, seen_update_ids, version`

// Save writes row under compare-and-swap.
//
// A zero row.Version means the caller's Load found no row: the write is an
// insert that REFUSES if a row has appeared since (another writer got there
// first). A non-zero Version updates only while the stored version still
// matches, advancing it by one.
func (s *Store) Save(ctx context.Context, row SubjectRow) error {
	if s == nil || s.db == nil {
		return cascade.New(cascade.KindUnavailable, "bridge: no database configured")
	}
	if row.Subject == "" {
		return cascade.New(cascade.KindInvalidInput, "bridge: a subject row needs a subject id")
	}
	allowed, err := encodeList(row.AllowedFrom)
	if err != nil {
		return err
	}
	seen, err := encodeList(row.SeenUpdateIDs)
	if err != nil {
		return err
	}
	if row.Version == 0 {
		return s.insertFirst(ctx, row, allowed, seen)
	}
	return s.updateAt(ctx, row, allowed, seen)
}

// insertFirst writes the first version of a row. DO NOTHING plus a rows-
// affected check, rather than DO UPDATE: a row that exists already was
// written by somebody whose state this caller never saw.
func (s *Store) insertFirst(ctx context.Context, row SubjectRow, allowed, seen string) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO `+tableSubject+` (`+subjectColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(subject) DO NOTHING`,
		row.Subject, row.TrustTier, millis(row.PairedAt), allowed, row.CodeDigest,
		millis(row.CodeExpiresAt), row.WrongAttempts, row.Offset, seen)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "bridge: write subject %q", row.Subject)
	}
	return conflictIfNoRows(res.RowsAffected())
}

// updateAt writes row only while the stored version is still the one the
// caller read, and advances it.
func (s *Store) updateAt(ctx context.Context, row SubjectRow, allowed, seen string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE `+tableSubject+` SET
			trust_tier = ?, paired_at = ?, allowed_from = ?, code_digest = ?,
			code_expires_at = ?, wrong_attempts = ?, poll_offset = ?, seen_update_ids = ?,
			version = version + 1
		WHERE subject = ? AND version = ?`,
		row.TrustTier, millis(row.PairedAt), allowed, row.CodeDigest,
		millis(row.CodeExpiresAt), row.WrongAttempts, row.Offset, seen,
		row.Subject, row.Version)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "bridge: write subject %q", row.Subject)
	}
	return conflictIfNoRows(res.RowsAffected())
}

// conflictIfNoRows maps "my statement matched nothing" onto the typed
// refusal. A driver that cannot report rows affected is treated as a
// conflict, not as a success: fail closed.
func conflictIfNoRows(affected int64, err error) error {
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "bridge: read back rows affected")
	}
	if affected == 0 {
		return ErrVersionConflict
	}
	return nil
}
