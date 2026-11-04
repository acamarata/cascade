// Purpose: white-box unit tests for encodeEvent/decodeEvent — round-trip
//
//	correctness and the specific truncation/overflow error paths
//	FuzzEventDecode (fuzz_test.go) explores exhaustively. In package
//	events (unexported functions). R-14.117-authorized split.
//
// SPORT: internal.events.Event/ADDED (envelope tests) (P1-E03-W1-S04-T3).
package events

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEncodeDecodeEvent_RoundTrip(t *testing.T) {
	cases := []Event{
		{Seq: 1, Kind: "k", Source: "s", Payload: []byte("payload"), Timestamp: time.Unix(1_700_000_000, 123456789).UTC()},
		{Seq: 0, Kind: "", Source: "", Payload: nil, Timestamp: time.Unix(0, 0).UTC()},
		{Seq: ^uint64(0), Kind: "unicode-中文-é", Source: "src", Payload: []byte{0x00, 0xff, 0x01}, Timestamp: time.Unix(0, 0).UTC()},
	}
	for i, want := range cases {
		encoded := encodeEvent(want)
		got, err := decodeEvent(encoded)
		if err != nil {
			t.Fatalf("case %d: decodeEvent(encodeEvent(...)) error: %v", i, err)
		}
		if got.Seq != want.Seq || got.Kind != want.Kind || got.Source != want.Source || !got.Timestamp.Equal(want.Timestamp) {
			t.Fatalf("case %d: round-trip mismatch: got %+v, want %+v", i, got, want)
		}
		if string(got.Payload) != string(want.Payload) {
			t.Fatalf("case %d: Payload mismatch: got %v, want %v", i, got.Payload, want.Payload)
		}
	}
}

func TestDecodeEvent_TruncatedHeader(t *testing.T) {
	_, err := decodeEvent([]byte{1, 2, 3})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEvent(truncated header) = %v, want KindIntegrity", err)
	}
}

func TestDecodeEvent_TruncatedLengthPrefix(t *testing.T) {
	buf := make([]byte, envelopeHeaderSize+2) // header + 2 bytes, short of a 4-byte length prefix
	_, err := decodeEvent(buf)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEvent(truncated length prefix) = %v, want KindIntegrity", err)
	}
}

func TestDecodeEvent_DeclaredLengthExceedsBuffer(t *testing.T) {
	buf := make([]byte, envelopeHeaderSize+envelopeLenSize)
	// Declare a Kind length far larger than any remaining bytes.
	buf[envelopeHeaderSize] = 0x7f
	buf[envelopeHeaderSize+1] = 0xff
	buf[envelopeHeaderSize+2] = 0xff
	buf[envelopeHeaderSize+3] = 0xff
	_, err := decodeEvent(buf)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEvent(oversized length prefix) = %v, want KindIntegrity", err)
	}
}

func TestDecodeEvent_TrailingBytes(t *testing.T) {
	encoded := encodeEvent(Event{Seq: 1, Kind: "k", Source: "s", Payload: nil, Timestamp: time.Unix(0, 0)})
	encoded = append(encoded, 0xde, 0xad)
	_, err := decodeEvent(encoded)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEvent(trailing bytes) = %v, want KindIntegrity", err)
	}
}

func TestDecodeEvent_Empty(t *testing.T) {
	_, err := decodeEvent(nil)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEvent(nil) = %v, want KindIntegrity", err)
	}
}

// TestEncodeDecodeEvent_StreamFieldsRoundTrip proves the stream extension
// survives the envelope codec field for field.
func TestEncodeDecodeEvent_StreamFieldsRoundTrip(t *testing.T) {
	want := Event{
		Seq: 7, Kind: "k", Source: "s", Payload: []byte("p"), Timestamp: time.Unix(5, 0).UTC(),
		StreamID: "task:one", StreamSeq: 3, RunID: "run-1", ProjectID: "proj-1", Producer: "jobs", PayloadVersion: 4,
	}
	got, err := decodeEvent(encodeEvent(want))
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}
	if got.StreamID != want.StreamID || got.StreamSeq != want.StreamSeq || got.RunID != want.RunID ||
		got.ProjectID != want.ProjectID || got.Producer != want.Producer || got.PayloadVersion != want.PayloadVersion {
		t.Fatalf("stream fields round-trip: got %+v, want %+v", got, want)
	}
}

// TestDecodeEvent_TruncatedStreamExtension proves every cut inside the
// stream extension is an integrity error, never a panic or a silent
// legacy read.
func TestDecodeEvent_TruncatedStreamExtension(t *testing.T) {
	full := encodeEvent(Event{Seq: 1, Kind: "k", StreamID: "s", StreamSeq: 1, Producer: "p"})
	base := len(encodeEvent(Event{Seq: 1, Kind: "k"})) - legacyExtensionSize()
	for cut := base + 1; cut < len(full); cut++ {
		if _, err := decodeEvent(full[:cut]); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("decodeEvent(cut at %d of %d) = %v, want KindIntegrity", cut, len(full), err)
		}
	}
}
