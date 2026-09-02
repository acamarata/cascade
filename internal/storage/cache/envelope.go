// Purpose: encode/decode the small binary envelope Cache persists through
//   provider.Store, carrying a value's absolute expiry instant alongside
//   its bytes so TTL survives the Store round-trip (Store itself has no
//   notion of expiry — see pkg/provider/store.go).
// Constraints: stdlib only (encoding/binary); no bare time.Now (the
//   envelope carries an already-resolved time.Time, computed by the
//   caller from an injected Clock).
// SPORT: internal.storage.cache.Cache/ADDED (P1-E02-W1-S02-T4).

package cache

import (
	"encoding/binary"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// envelopeHeaderSize is the fixed-width prefix (an int64 UnixNano expiry
// timestamp) every persisted entry carries ahead of its raw value bytes.
const envelopeHeaderSize = 8

// encodeEnvelope packs expiresAt (the zero time.Time means "no expiry")
// and value into the on-disk/on-Store representation.
func encodeEnvelope(expiresAt time.Time, value []byte) []byte {
	var nanos int64
	if !expiresAt.IsZero() {
		nanos = expiresAt.UnixNano()
	}
	buf := make([]byte, envelopeHeaderSize+len(value))
	binary.BigEndian.PutUint64(buf[:envelopeHeaderSize], uint64(nanos))
	copy(buf[envelopeHeaderSize:], value)
	return buf
}

// decodeEnvelope unpacks a value previously produced by encodeEnvelope. A
// zero result expiresAt means "no expiry". decodeEnvelope returns a
// cascade.KindIntegrity error if buf is shorter than the fixed header,
// which never happens for entries this package itself wrote but guards
// against a corrupted or foreign Store record.
func decodeEnvelope(buf []byte) (expiresAt time.Time, value []byte, err error) {
	if len(buf) < envelopeHeaderSize {
		return time.Time{}, nil, cascade.Newf(cascade.KindIntegrity, "cache: envelope truncated (%d bytes, want at least %d)", len(buf), envelopeHeaderSize)
	}
	nanos := int64(binary.BigEndian.Uint64(buf[:envelopeHeaderSize]))
	if nanos != 0 {
		expiresAt = time.Unix(0, nanos)
	}
	value = append([]byte(nil), buf[envelopeHeaderSize:]...)
	return expiresAt, value, nil
}
