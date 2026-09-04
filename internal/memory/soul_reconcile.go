package memory

// Purpose: route (b), reconcile-on-load: comparing the document on disk
//   against the digest the store recorded, adopting a clean out-of-store
//   edit through the audited path, and refusing a conflict loudly — a bus
//   event and a diagnostic note — rather than merging or discarding.
// Inputs: the ledger and the document file; the injected Clock.
// Outputs: a DivergenceResult, a DivergenceEvent on the sink, a note file,
//   and pkg/cascade taxonomy errors.
// Constraints: nothing this file emits or writes carries any soul text —
//   an event and a note travel to readers that have no business seeing the
//   user's identity document; no automatic merge exists here at all.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// soulNoteFile is the diagnostic note a conflict leaves behind, read by
// `cascade doctor`. It sits beside the document so a conflict is
// discoverable from the tree itself and does not depend on anyone having
// been subscribed to the bus at the moment it happened.
const soulNoteFile = "divergence-note.json"

// soulDivergenceNote is what that file holds. It is deliberately the
// DivergenceEvent's fields plus a remediation line: a diagnostic note is
// pasted into bug reports, so it names versions and digests and never one
// byte of the document either side holds.
type soulDivergenceNote struct {
	// DetectedAt is when the conflict was found, in UTC.
	DetectedAt time.Time `json:"detected_at"`
	// Version and LastReconciledVersion locate the conflict.
	Version               int `json:"version"`
	LastReconciledVersion int `json:"last_reconciled_version"`
	// StoredHash and FileHash identify the two sides.
	StoredHash string `json:"stored_hash"`
	FileHash   string `json:"file_hash"`
	// Remediation is the human instruction. It is a fixed sentence, not a
	// rendered path: a note that named the file would carry the
	// operator's home directory into every paste of it.
	Remediation string `json:"remediation"`
}

// soulNoteRemediation is that fixed sentence.
const soulNoteRemediation = "the soul document and the store both changed since the last " +
	"reconcile; neither side was applied. Review the document, then re-apply the " +
	"version you want with `cascade memory soul edit`."

// notePath is the conflict note's location.
func (s *FileSoulStore) notePath() string {
	return filepath.Join(s.base, soulDir, soulNoteFile)
}

// isSoulConflict reports whether err is the divergence refusal. Callers
// that can proceed despite an unresolved conflict — Get, which reports it
// through SoulView.Diverged, and the explicit write paths, which record it
// and then honour the caller's instruction — use it to tell a conflict
// apart from a real failure.
func isSoulConflict(err error) bool { return errors.Is(err, ErrSoulDiverged) }

// DetectDivergence performs route (b).
//
// # The three outcomes
//
//   - The digests agree: nothing happened. No version moves, no entry is
//     appended, no event is emitted; only the bookkeeping pointer that
//     records "the two sides were seen to agree" moves (confirmAgreement).
//   - The file changed and the store has not written since the last
//     reconcile: the file's content is what the user meant, and it is
//     adopted through applyEdit(route=file-reconcile) — version bumped,
//     audit entry appended, digest updated. The user's own edit becomes a
//     recorded edit rather than an untracked one.
//   - The file changed AND the store has written since the last reconcile:
//     both sides moved and the store cannot know whether the editor had
//     the store's version in hand. Nothing is merged, nothing is adopted,
//     nothing is discarded. A typed event goes to the bus, a note is left
//     for `cascade doctor`, and the call refuses with ErrSoulDiverged.
//
// An out-of-store edit made before the store's own last write was ever
// seen on disk is therefore reported as a conflict even when the editor
// did have the newer text in hand. That is the conservative reading on
// purpose: the store has no way to tell those two situations apart, and
// the cost of being wrong is destroying what a person wrote about
// themselves. In ordinary use the daemon reconciles at start and every
// `soul show` reconciles again, so the store's writes are confirmed long
// before anyone opens the file, and an editor session is a clean edit.
func (s *FileSoulStore) DetectDivergence(ctx context.Context) (DivergenceResult, error) {
	if err := ctx.Err(); err != nil {
		return DivergenceResult{}, cascade.Wrap(cascade.KindCanceled, err,
			"soul divergence check canceled")
	}
	led, err := s.loadLedger()
	if err != nil {
		return DivergenceResult{}, err
	}
	body, found, err := s.readDocument()
	if err != nil {
		return DivergenceResult{}, err
	}
	res := DivergenceResult{
		Outcome:               DivergenceNone,
		Version:               led.Version,
		LastReconciledVersion: led.LastReconciledVersion,
		StoredHash:            led.ContentHash,
	}
	if found {
		res.FileHash = HashBody(body)
	}
	if !found {
		return s.absentDocument(ctx, led, res)
	}
	if res.FileHash == led.ContentHash {
		return s.confirmAgreement(led, res)
	}
	return s.reconcile(ctx, led, res, body)
}

// confirmAgreement records that the file and the store were seen to agree.
//
// The document, the version and the audit log are untouched — this is the
// contract's no-op branch and it appends nothing and changes nothing a
// reader can see. What it does move is the bookkeeping pointer, and it has
// to: LastReconciledVersion means "the version at which the two sides were
// last known to agree", and they are agreeing right now. Leaving it behind
// would make every later out-of-store edit look like a conflict, because
// the store would still be treating its own last write as unconfirmed.
// That is also what keeps the conflict branch meaningful rather than
// universal: a conflict is a file edit made when the store's last write
// had NOT been seen on disk, which is exactly the case where the editor
// may not have had it.
func (s *FileSoulStore) confirmAgreement(
	led soulLedger, res DivergenceResult,
) (DivergenceResult, error) {
	if led.LastReconciledVersion == led.Version {
		return res, nil
	}
	led.LastReconciledVersion = led.Version
	led.UpdatedAt = s.clock.Now().UTC()
	if err := s.persistLedger(led); err != nil {
		return DivergenceResult{}, err
	}
	res.LastReconciledVersion = led.Version
	return res, nil
}

// absentDocument decides what a missing file means.
//
// Before the first write it means nothing has been written yet, which is a
// normal state. After a write it means the identity document was removed
// out from under the store, and that is a conflict rather than an adopted
// deletion: silently accepting it would leave the store holding a version
// and an audit log for a document it no longer has, and re-writing the
// file from the store would undo a deletion the user may have meant.
func (s *FileSoulStore) absentDocument(
	ctx context.Context, led soulLedger, res DivergenceResult,
) (DivergenceResult, error) {
	if led.Version == 0 {
		return res, nil
	}
	return s.conflict(ctx, res)
}

// reconcile adopts a clean out-of-store edit, or reports a conflict.
func (s *FileSoulStore) reconcile(
	ctx context.Context, led soulLedger, res DivergenceResult, body string,
) (DivergenceResult, error) {
	if led.Version != led.LastReconciledVersion {
		return s.conflict(ctx, res)
	}
	view, err := s.applyEdit(ctx, led.document(body), SoulRouteFileReconcile)
	if err != nil {
		return DivergenceResult{}, err
	}
	res.Outcome = DivergenceReconciled
	res.Version = view.Version
	res.LastReconciledVersion = view.Version
	res.StoredHash = res.FileHash
	return res, nil
}

// conflict reports an unresolved divergence: it emits the typed event,
// leaves the diagnostic note, and refuses.
//
// The note is written before the event is published so a conflict is
// discoverable from the tree even if no subscriber was listening, and a
// failure to write the note does not swallow the refusal: the caller is
// told about the divergence either way, because a conflict reported as an
// I/O error would look like a transient fault rather than a document that
// must not be written over.
func (s *FileSoulStore) conflict(
	ctx context.Context, res DivergenceResult,
) (DivergenceResult, error) {
	res.Outcome = DivergenceConflict
	now := s.clock.Now().UTC()
	if err := s.writeNote(res, now); err != nil {
		return res, err
	}
	ev := DivergenceEvent{
		Version:               res.Version,
		LastReconciledVersion: res.LastReconciledVersion,
		StoredHash:            res.StoredHash,
		FileHash:              res.FileHash,
		DetectedAt:            now,
	}
	if err := s.sink.SoulDiverged(ctx, ev); err != nil {
		return res, err
	}
	return res, cascade.Wrapf(cascade.KindConflict, ErrSoulDiverged,
		"soul document and store both changed since version %d", res.LastReconciledVersion)
}

// writeNote leaves the diagnostic note for `cascade doctor`.
func (s *FileSoulStore) writeNote(res DivergenceResult, now time.Time) error {
	note := soulDivergenceNote{
		DetectedAt:            now,
		Version:               res.Version,
		LastReconciledVersion: res.LastReconciledVersion,
		StoredHash:            res.StoredHash,
		FileHash:              res.FileHash,
		Remediation:           soulNoteRemediation,
	}
	data, err := json.Marshal(note)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, ErrStoreIO,
			"encoding soul divergence note: %v", err)
	}
	if err := s.fs.WriteAtomic(s.notePath(), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing soul divergence note: %v", err)
	}
	return nil
}
