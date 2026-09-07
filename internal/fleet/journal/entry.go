package journal

// Purpose: the entry-integrity half of the journal schema: the BLAKE3
//
//	content checksum that seals an entry, the verification every read
//	runs before returning one, and the fail-closed validation an
//	Append call must pass before it is ever written.
//
// Inputs: an Entry built by Append; stored bytes read back from the
//
//	provider.Store.
//
// Outputs: a sealed or verified Entry, or a pkg/cascade taxonomy
//
//	error.
//
// Constraints: the checksum/validation half of this file is pure
//
//	functions only, no clock, no I/O and no randomness; the torn-tail
//	recovery functions at the bottom of this file do read and write
//	through a provider.Store (see their own doc comments).
//
// SPORT: internal.fleet.journal.Entry/ADDED (P1-E13-W3-S27-T1).

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// contentChecksum computes an entry's BLAKE3 checksum over the canonical
// encoding of every field but Checksum itself. Encoding is encoding/json
// over a struct, whose field order is its declaration order, so the digest
// is deterministic across runs and builds without a separate
// canonicalization pass (the identical technique internal/audit's
// contentHash uses for the same reason).
func contentChecksum(e Entry) (string, error) {
	e.Checksum = ""
	encoded, err := json.Marshal(e)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "journal: encoding entry for checksum")
	}
	sum := blake3.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// seal fills in an entry's Checksum. Called once, by the append path,
// after every other field is final.
func seal(e Entry) (Entry, error) {
	sum, err := contentChecksum(e)
	if err != nil {
		return Entry{}, err
	}
	e.Checksum = sum
	return e, nil
}

// verifyEntry recomputes a decoded entry's checksum and refuses it on a
// mismatch. This is what turns an on-disk alteration, or a torn write,
// into a detected failure instead of an accepted answer.
func verifyEntry(e Entry) error {
	if !e.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownKind, "entity %s seq %d: kind %d", e.EntityID, e.Seq, e.Kind)
	}
	want, err := contentChecksum(e)
	if err != nil {
		return err
	}
	if want != e.Checksum {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"entity %s seq %d: checksum mismatch", e.EntityID, e.Seq)
	}
	return nil
}

// decodeEntry parses stored bytes and verifies the entry's own checksum
// before returning it. No caller in this package sees an unverified entry:
// a damaged, truncated, or rewritten record is refused whole rather than
// returned partially trusted.
func decodeEntry(data []byte) (Entry, error) {
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"stored entry is not decodable: %v", err)
	}
	if err := verifyEntry(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// tryDecodeEntry is decodeEntry without failing the caller: it reports ok
// == false for anything verifyEntry or json.Unmarshal would refuse, which
// is exactly what the torn-tail scan needs — "the last entry whose
// checksum verifies" is found by trying to decode forward and stopping at
// the first entry that does not.
func tryDecodeEntry(data []byte) (Entry, bool) {
	e, err := decodeEntry(data)
	if err != nil {
		return Entry{}, false
	}
	return e, true
}

// validateAppendInput refuses an Append call that must not reach the log:
// an empty entity id, an empty operation id, an invalid kind, or a payload
// that is present but not valid JSON.
func validateAppendInput(entityID string, kind Kind, operationID string, payload json.RawMessage) error {
	if entityID == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "entity id is required")
	}
	if operationID == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "operation id is required")
	}
	if !kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownKind, "kind %d", kind)
	}
	if len(payload) > 0 && !json.Valid(payload) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "payload is not valid JSON")
	}
	return nil
}

// The torn-tail recovery functions below (recoverEntityLocked,
// scanAndTruncate, repairHead) are declared in this file rather than
// store.go under R-14.117's authorized-split allowance (Art.10.3's
// 300-line-per-file cap; store.go already carries Append and its
// transactional commit path). Recovery is entry-integrity work — it walks
// entries verifying the same checksum entry.go's verifyEntry checks — so
// this is also the more cohesive home for it.
// recoverEntity runs entityID's torn-tail scan exactly once per SQLiteStore
// instance: it walks the log forward from sequence 1, verifying each
// entry's checksum, deletes the first entry (and everything the scan
// still finds after it) whose checksum does not verify, and repairs the
// head pointer to the last entry that did. See this file's package doc
// for why forward-from-1 finds the same boundary a backward scan would.
//
// The caller MUST already hold s.lockFor(entityID) — Recover and Append
// both acquire it before calling this, so a recovery scan for one entity
// can never interleave with an Append that is concurrently allocating that
// same entity's next sequence number.
func (s *SQLiteStore) recoverEntityLocked(ctx context.Context, entityID string) (TruncationReport, error) {
	s.mu.Lock()
	if r, ok := s.recovered[entityID]; ok {
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()

	lastGood, report, err := s.scanAndTruncate(ctx, entityID)
	if err != nil {
		return TruncationReport{}, err
	}
	if err := s.repairHead(ctx, entityID, lastGood); err != nil {
		return TruncationReport{}, err
	}

	s.mu.Lock()
	s.recovered[entityID] = report
	s.mu.Unlock()
	return report, nil
}

// scanAndTruncate walks entityID's entries from seq 1 and deletes the
// first bad one and every subsequent seq the scan still finds present.
func (s *SQLiteStore) scanAndTruncate(ctx context.Context, entityID string) (lastGood uint64, report TruncationReport, err error) {
	for seq := uint64(1); ; seq++ {
		data, gerr := s.store.Get(ctx, s.namespace, entryKey(entityID, seq))
		if gerr != nil {
			if cascade.HasKind(gerr, cascade.KindNotFound) {
				return lastGood, report, nil
			}
			return 0, TruncationReport{}, wrapStore(gerr, "journal: recovery scan")
		}
		if _, ok := tryDecodeEntry(data); !ok {
			if report.Truncated == 0 {
				report.FirstBadSeq = seq
			}
			report.Truncated++
			if derr := s.store.Delete(ctx, s.namespace, entryKey(entityID, seq)); derr != nil {
				return 0, TruncationReport{}, wrapStore(derr, "journal: recovery delete")
			}
			continue
		}
		lastGood = seq
	}
}

// repairHead lowers entityID's head pointer to lastGood when the two
// disagree, which happens only when scanAndTruncate found and removed
// corrupted trailing entries the head pointer still counted.
func (s *SQLiteStore) repairHead(ctx context.Context, entityID string, lastGood uint64) error {
	cur, err := s.loadHeadFrom(ctx, s.store, entityID)
	if err != nil {
		return err
	}
	if cur == lastGood {
		return nil
	}
	data, err := json.Marshal(headRecord{Seq: lastGood})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "journal: encoding repaired head")
	}
	if err := s.store.Put(ctx, s.namespace, headKey(entityID), data); err != nil {
		return wrapStore(err, "journal: repairing head")
	}
	return nil
}
