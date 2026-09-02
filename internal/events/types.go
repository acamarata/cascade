// Purpose: Event, Kind, and the wire envelope this ticket persists through
//
//	provider.Store — encodeEvent/decodeEvent are pure, allocation-owning
//	functions with no I/O, so they can be exercised directly by
//	fuzz_test.go's FuzzEventDecode with no Store or Bus involved.
//
// Inputs: encodeEvent takes a fully-populated Event; decodeEvent takes
//
//	arbitrary bytes, including adversarial/truncated ones from the fuzzer.
//
// Outputs: encodeEvent never fails (any Event value is encodable);
//
//	decodeEvent returns a cascade.KindIntegrity error — never a panic —
//	for any input that is not a well-formed envelope it produced itself.
//
// Constraints: no bare time.Now (Timestamp is caller-supplied, always
//
//	sourced from Bus's injected runtime.Clock — see bus.go); every length
//	read is bounds-checked before any slice operation, so a truncated or
//	hostile buffer can only ever produce an error return, never an
//	out-of-range panic (this is exactly what FuzzEventDecode proves).
//
// SPORT: internal.events.Event/ADDED, internal.events.EventKind/ADDED
//
//	(P1-E03-W1-S04-T3).

package events

import (
	"encoding/binary"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EventKind identifies the semantic type of one event's payload. Unlike
// pkg/cascade.Kind (a closed, 14-member taxonomy frozen by T0 ruling
// R-14.3), EventKind is deliberately OPEN: internal/events is generic
// pub/sub infrastructure that many independently-owned future producers —
// the scheduler, hooks, doctor, crash recovery, memory consolidation — mint
// their own EventKind values against, with no need for an amendment to
// this package each time. EventKind is still a defined string type (never
// a bare string at a call site) so a value is self-documenting in logs and
// in Subscribe/Replay call sites.
type EventKind string

// EventKindPluginRegistered is emitted once a plugin completes
// registration. It is the one EventKind this ticket wires end to end
// itself: the R-14.134 obligation completes internal/plugins' integration
// test by publishing this event through a real Bus and asserting real
// delivery — see internal/plugins/registry_integration_test.go.
const EventKindPluginRegistered EventKind = "plugin.registered"

// Event is one typed, persisted entry in a Bus namespace's ordered log.
type Event struct {
	// Seq is the event's 1-based position within its namespace's log,
	// assigned by Publish. Sequence numbers are strictly increasing and
	// gapless per namespace. 0 is never a real Seq — it is the cursor
	// sentinel meaning "before the first event" (cursor.go).
	Seq uint64
	// Kind identifies the event's semantic type.
	Kind EventKind
	// Payload is the caller-supplied event body, opaque to the bus.
	Payload []byte
	// Source identifies the publisher (e.g. a plugin ID, "scheduler").
	Source string
	// Timestamp is the instant Publish recorded, read from the Bus's
	// injected runtime.Clock — never a bare time.Now (R-14.11).
	Timestamp time.Time
}

// clone returns a copy of e whose Payload slice is independently owned, so
// a caller mutating a slice it received from Subscribe/Replay (or passed
// into Publish) can never corrupt the bus's own state or another
// recipient's copy.
func (e Event) clone() Event {
	if e.Payload == nil {
		return e
	}
	cp := make([]byte, len(e.Payload))
	copy(cp, e.Payload)
	e.Payload = cp
	return e
}

// Envelope wire format (encodeEvent/decodeEvent), all integers big-endian:
//
//	[8]  Seq
//	[8]  Timestamp, as UnixNano
//	[4+N] Kind:    4-byte length prefix, then N raw bytes
//	[4+N] Source:  4-byte length prefix, then N raw bytes
//	[4+N] Payload: 4-byte length prefix, then N raw bytes
const (
	envelopeSeqSize    = 8
	envelopeTimeSize   = 8
	envelopeHeaderSize = envelopeSeqSize + envelopeTimeSize
	envelopeLenSize    = 4
)

// encodeEvent serializes e into the Store-persisted wire format. It never
// fails — every Event value, including a zero Kind/Source/nil Payload, has
// a valid encoding.
func encodeEvent(e Event) []byte {
	kindB := []byte(e.Kind)
	sourceB := []byte(e.Source)
	total := envelopeHeaderSize +
		envelopeLenSize + len(kindB) +
		envelopeLenSize + len(sourceB) +
		envelopeLenSize + len(e.Payload)

	buf := make([]byte, envelopeHeaderSize, total)
	binary.BigEndian.PutUint64(buf[:envelopeSeqSize], e.Seq)
	binary.BigEndian.PutUint64(buf[envelopeSeqSize:envelopeHeaderSize], uint64(e.Timestamp.UnixNano()))
	buf = appendLenPrefixed(buf, kindB)
	buf = appendLenPrefixed(buf, sourceB)
	buf = appendLenPrefixed(buf, e.Payload)
	return buf
}

// decodeEvent deserializes data produced by encodeEvent. It returns a
// cascade.KindIntegrity error — never a panic — for any input shorter
// than, or structurally inconsistent with, the envelope format: a
// corrupted Store record or a foreign/hostile byte string (this is the
// property FuzzEventDecode proves).
func decodeEvent(data []byte) (Event, error) {
	if len(data) < envelopeHeaderSize {
		return Event{}, cascade.Newf(cascade.KindIntegrity,
			"events: envelope truncated (%d byte(s), want at least %d)", len(data), envelopeHeaderSize)
	}
	seq := binary.BigEndian.Uint64(data[:envelopeSeqSize])
	tsNano := int64(binary.BigEndian.Uint64(data[envelopeSeqSize:envelopeHeaderSize]))
	rest := data[envelopeHeaderSize:]

	kindB, rest, err := readLenPrefixed(rest)
	if err != nil {
		return Event{}, err
	}
	sourceB, rest, err := readLenPrefixed(rest)
	if err != nil {
		return Event{}, err
	}
	payloadB, rest, err := readLenPrefixed(rest)
	if err != nil {
		return Event{}, err
	}
	if len(rest) != 0 {
		return Event{}, cascade.Newf(cascade.KindIntegrity, "events: envelope has %d trailing byte(s)", len(rest))
	}

	return Event{
		Seq:       seq,
		Kind:      EventKind(kindB),
		Source:    string(sourceB),
		Payload:   payloadB,
		Timestamp: time.Unix(0, tsNano).UTC(),
	}, nil
}

// appendLenPrefixed appends a 4-byte big-endian length prefix and v itself
// to buf.
func appendLenPrefixed(buf, v []byte) []byte {
	var lenBuf [envelopeLenSize]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(v)))
	buf = append(buf, lenBuf[:]...)
	return append(buf, v...)
}

// readLenPrefixed reads one length-prefixed section from the front of b,
// returning the section's owned-copy bytes and the remaining tail. Every
// length is bounds-checked against the actual remaining buffer BEFORE any
// slice operation, so adversarial input can only produce an error, never
// an out-of-range panic.
func readLenPrefixed(b []byte) (value, rest []byte, err error) {
	if len(b) < envelopeLenSize {
		return nil, nil, cascade.Newf(cascade.KindIntegrity,
			"events: envelope truncated reading length prefix (%d byte(s) left)", len(b))
	}
	n := binary.BigEndian.Uint32(b[:envelopeLenSize])
	b = b[envelopeLenSize:]
	if uint64(n) > uint64(len(b)) {
		return nil, nil, cascade.Newf(cascade.KindIntegrity,
			"events: envelope declares length %d but only %d byte(s) remain", n, len(b))
	}
	value = append([]byte(nil), b[:n]...)
	return value, b[n:], nil
}

// parseEventSeq extracts the Seq encoded in an "event:"-prefixed Store key
// (bus.go's eventKey format), used by recoverMaxSeq to reconstruct a
// namespace's sequence counter from Store state alone (the "simulated
// restart" path — no in-memory state survives a real process restart).
func parseEventSeq(key string) (uint64, error) {
	if !strings.HasPrefix(key, eventKeyPrefix) {
		return 0, cascade.Newf(cascade.KindIntegrity, "events: scanned key %q missing prefix %q", key, eventKeyPrefix)
	}
	digits := strings.TrimPrefix(key, eventKeyPrefix)
	seq, convErr := strconv.ParseUint(digits, 10, 64)
	if convErr != nil {
		return 0, cascade.Wrapf(cascade.KindIntegrity, convErr, "events: scanned key %q has malformed sequence suffix", key)
	}
	return seq, nil
}
