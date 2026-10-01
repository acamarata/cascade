// Purpose: the durable, versioned per-message record Queue persists
//   through provider.Store: payload, attempt count,
//   ready/claimed state, receipt and visibility deadline as ONE Store
//   value, plus the read-time migration of a legacy body-only record
//   (envelope.go's pre-existing encodeEnvelope/decodeEnvelope format) into
//   this shape, and namespace recovery -- scanning a namespace's
//   persisted "msg:" records into a fresh namespaceState the first time a
//   Queue instance touches that namespace.
// Inputs: a record's logical fields (encodeRecord); raw Store bytes
//   (decodeRecord, migrateLegacy); a provider.Store + runtime.Clock and a
//   namespace (recoverNamespace).
// Outputs: the Store-ready []byte encoding, a decoded record, or a
//   populated *namespaceState.
// Constraints: no bare time.Now (clock.Now() only, passed in); every ID
//   this package generates (ids.go) is a zero-padded monotonic sequence
//   plus a random suffix, and provider.Iterator's own doc comment says
//   Scan walks results "in key order" -- so scanning "msg:" already
//   yields original enqueue order; recovery needs no separate ordering
//   field or sort.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue

import (
	"bytes"
	"context"
	"encoding/binary"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordMagic marks a "msg:" value as this change's versioned record
// format, distinguishing it from a legacy body-only record (envelope.go's
// pre-existing 4-byte-attempts-plus-payload format, which carries no such
// marker). A legacy record's first 4 bytes are a big-endian attempt
// count; colliding with recordMagic would require an attempt counter past
// 1.36 billion, which no real MaxAttempts config or redelivery history
// reaches -- the documented deterministic migration rule.
var recordMagic = [4]byte{'Q', 'R', 'C', '1'}

const (
	recordStateReady   = byte(0)
	recordStateClaimed = byte(1)
	// recordHeaderSize: magic(4) + state(1) + attempts(4) +
	// deadlineUnixNano(8) + receiptLen(2), ahead of the variable-length
	// receipt then payload tail.
	recordHeaderSize = 4 + 1 + 4 + 8 + 2
)

// record is the fully-decoded shape of one durable queue-message entry.
type record struct {
	claimed  bool
	attempts uint32
	deadline time.Time // valid iff claimed
	receipt  string    // valid iff claimed
	payload  []byte
}

// encodeRecord packs rec into its Store-ready bytes.
func encodeRecord(rec record) []byte {
	state := recordStateReady
	var deadlineNano int64
	receipt := ""
	if rec.claimed {
		state = recordStateClaimed
		deadlineNano = rec.deadline.UnixNano()
		receipt = rec.receipt
	}
	buf := make([]byte, recordHeaderSize+len(receipt)+len(rec.payload))
	copy(buf[0:4], recordMagic[:])
	buf[4] = state
	binary.BigEndian.PutUint32(buf[5:9], rec.attempts)
	binary.BigEndian.PutUint64(buf[9:17], uint64(deadlineNano))
	binary.BigEndian.PutUint16(buf[17:19], uint16(len(receipt)))
	copy(buf[19:19+len(receipt)], receipt)
	copy(buf[19+len(receipt):], rec.payload)
	return buf
}

// decodeRecord unpacks buf as this change's versioned format. ok is false
// (with a nil error) ONLY when buf carries no recordMagic at all -- the
// caller's signal to fall back to migrateLegacy rather than treating a
// foreign buffer as a real decode error. A buffer that DOES start with
// recordMagic but is shorter than a full header is never silently handed
// to migrateLegacy: that would let a truncated/corrupted modern record be
// reinterpreted as a legacy body (its magic bytes as a bogus attempt
// count, whatever bytes remain as payload) and migrated into a normal,
// deliverable message -- a fail-open bug, not a recovery. It is instead a
// hard KindIntegrity error, the fail-closed probe this change owes
// (TestQueue_TruncatedModernRecordFailsClosed).
func decodeRecord(buf []byte) (rec record, ok bool, err error) {
	if len(buf) < 4 || !bytes.Equal(buf[0:4], recordMagic[:]) {
		return record{}, false, nil
	}
	if len(buf) < recordHeaderSize {
		return record{}, false, cascade.Newf(cascade.KindIntegrity, "queue: record truncated (%d bytes, want at least %d for the header)", len(buf), recordHeaderSize)
	}
	state := buf[4]
	attempts := binary.BigEndian.Uint32(buf[5:9])
	deadlineNano := int64(binary.BigEndian.Uint64(buf[9:17]))
	receiptLen := int(binary.BigEndian.Uint16(buf[17:19]))
	if recordHeaderSize+receiptLen > len(buf) {
		return record{}, false, cascade.Newf(cascade.KindIntegrity, "queue: record receipt length %d exceeds buffer", receiptLen)
	}
	receipt := string(buf[recordHeaderSize : recordHeaderSize+receiptLen])
	payload := append([]byte(nil), buf[recordHeaderSize+receiptLen:]...)
	rec = record{attempts: attempts, payload: payload}
	if state == recordStateClaimed {
		rec.claimed = true
		rec.receipt = receipt
		rec.deadline = time.Unix(0, deadlineNano).UTC()
	}
	return rec, true, nil
}

// migrateLegacy synthesizes a ready-state record from a legacy
// (envelope.go) body-only record: attempts carries over unchanged,
// receipt/deadline are unset (never claimed) -- ready ordering is Scan's
// key order, contributed by recoverNamespace, not this function. Pure:
// two callers migrating the same legacy bytes always synthesize
// byte-identical output, which is what makes recoverOneRecord's
// losing-CAS path safe to just re-read rather than retry.
func migrateLegacy(raw []byte) (record, error) {
	attempts, payload, err := decodeEnvelope(raw)
	if err != nil {
		return record{}, err
	}
	return record{attempts: attempts, payload: payload}, nil
}

// recoverNamespace scans ns's persisted "msg:" records and returns a
// namespaceState populated to match: ready ids in Scan's key order (which
// is original enqueue order), inflight claims still within their
// deadline, and every record's knownBytes -- the Store.Tx "prior bytes"
// baseline every later transition (queue.go, ack.go) CAS/delete-fences
// against, so this Queue instance never re-Gets a record between recovery
// and its first mutation of it.
func recoverNamespace(ctx context.Context, store provider.Store, clock runtime.Clock, ns string) (*namespaceState, error) {
	it, err := store.Scan(ctx, ns, msgKeyPrefix)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "queue: recovering namespace %q", ns)
	}
	defer func() { _ = it.Close() }()

	st := newNamespaceState()
	now := clock.Now()
	for it.Next(ctx) {
		id := it.Key()[len(msgKeyPrefix):]
		rec, knownBytes, recErr := recoverOneRecord(ctx, store, ns, it.Key(), it.Value())
		if recErr != nil {
			return nil, recErr
		}
		st.trackRecovered(id, rec, knownBytes, now)
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "queue: recovering namespace %q: scan", ns)
	}
	return st, nil
}

// recoverOneRecord decodes raw (as read by recoverNamespace's Scan) into a
// record plus the exact bytes this Queue now believes are persisted at
// key. A legacy body-only record is migrated in place: one Store.Tx
// CompareAndSwap from raw to the synthesized new-format bytes, CAS-fenced
// the same as any other write. A losing CAS (another
// instance migrated the same record first) is not an error -- migration
// is pure, so both instances compute byte-identical output; this re-reads
// and returns the now-current bytes instead of retrying blindly.
func recoverOneRecord(ctx context.Context, store provider.Store, ns, key string, raw []byte) (record, []byte, error) {
	if rec, ok, err := decodeRecord(raw); err != nil {
		return record{}, nil, cascade.Wrapf(cascade.KindIntegrity, err, "queue: decoding %q in namespace %q", key, ns)
	} else if ok {
		return rec, raw, nil
	}
	rec, err := migrateLegacy(raw)
	if err != nil {
		return record{}, nil, cascade.Wrapf(cascade.KindIntegrity, err, "queue: migrating legacy record %q in namespace %q", key, ns)
	}
	migrated := encodeRecord(rec)
	txErr := store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, ns, key, raw, migrated)
	})
	return afterMigrationTx(ctx, store, ns, key, rec, migrated, txErr)
}

// afterMigrationTx resolves recoverOneRecord's migration CAS outcome,
// split out to keep recoverOneRecord under the 50-line cap.
func afterMigrationTx(ctx context.Context, store provider.Store, ns, key string, rec record, migrated []byte, txErr error) (record, []byte, error) {
	switch {
	case txErr == nil:
		return rec, migrated, nil
	case cascade.HasKind(txErr, cascade.KindConflict):
		current, getErr := store.Get(ctx, ns, key)
		if getErr != nil {
			return record{}, nil, cascade.Wrapf(cascade.KindUnavailable, getErr, "queue: re-reading %q in namespace %q after migration race", key, ns)
		}
		if curRec, ok, decErr := decodeRecord(current); decErr == nil && ok {
			return curRec, current, nil
		}
		return record{}, nil, cascade.Newf(cascade.KindIntegrity, "queue: %q in namespace %q changed to an unrecognized format during migration", key, ns)
	default:
		return record{}, nil, cascade.Wrapf(cascade.KindUnavailable, txErr, "queue: migrating legacy record %q in namespace %q", key, ns)
	}
}
