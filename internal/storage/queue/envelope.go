// Purpose: encode/decode the small binary envelope Queue persists through
//   provider.Store for one message: its payload plus the delivery-attempt
//   counter used for the MaxAttempts/DLQ decision. Purely a serialization
//   helper — no Queue business logic lives here.
// Constraints: stdlib only (encoding/binary).
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).

package queue

import (
	"encoding/binary"

	"github.com/acamarata/cascade/pkg/cascade"
)

// envelopeHeaderSize is the fixed-width prefix (a uint32 attempt counter)
// every persisted message record carries ahead of its raw payload bytes.
const envelopeHeaderSize = 4

// encodeEnvelope packs attempts and payload into the on-Store
// representation.
func encodeEnvelope(attempts uint32, payload []byte) []byte {
	buf := make([]byte, envelopeHeaderSize+len(payload))
	binary.BigEndian.PutUint32(buf[:envelopeHeaderSize], attempts)
	copy(buf[envelopeHeaderSize:], payload)
	return buf
}

// decodeEnvelope unpacks a record previously produced by encodeEnvelope.
// It returns a cascade.KindIntegrity error if buf is shorter than the
// fixed header, which never happens for records this package itself wrote
// but guards against a corrupted or foreign Store record.
func decodeEnvelope(buf []byte) (attempts uint32, payload []byte, err error) {
	if len(buf) < envelopeHeaderSize {
		return 0, nil, cascade.Newf(cascade.KindIntegrity, "queue: envelope truncated (%d bytes, want at least %d)", len(buf), envelopeHeaderSize)
	}
	attempts = binary.BigEndian.Uint32(buf[:envelopeHeaderSize])
	payload = append([]byte(nil), buf[envelopeHeaderSize:]...)
	return attempts, payload, nil
}
