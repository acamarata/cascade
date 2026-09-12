// Purpose: unit + fuzz coverage for chunk.go's frame encode/decode.
// SPORT: internal.sync.chunk/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"bytes"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	c := Chunk{StreamID: 42, Seq: 1, Total: 3, Payload: []byte("hello sync")}
	var buf bytes.Buffer
	if err := Encode(&buf, c); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.StreamID != c.StreamID || got.Seq != c.Seq || got.Total != c.Total || !bytes.Equal(got.Payload, c.Payload) {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, c)
	}
}

func TestDecodeTruncatedHeaderFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("x")})
	truncated := buf.Bytes()[:headerLen-3]
	if _, err := Decode(bytes.NewReader(truncated)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("truncated header: want KindIntegrity, got %v", err)
	}
}

func TestDecodeTruncatedPayloadFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("hello")})
	truncated := buf.Bytes()[:headerLen+2]
	if _, err := Decode(bytes.NewReader(truncated)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("truncated payload: want KindIntegrity, got %v", err)
	}
}

func TestDecodeWrongMagicFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("x")})
	raw := buf.Bytes()
	raw[0] = 'Z'
	if _, err := Decode(bytes.NewReader(raw)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("wrong magic: want KindIntegrity, got %v", err)
	}
}

func TestDecodeCorruptedPayloadHashFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("original")})
	raw := buf.Bytes()
	raw[len(raw)-1] ^= 0xFF // flip the last payload byte after hashing/framing
	if _, err := Decode(bytes.NewReader(raw)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupted payload: want KindIntegrity, got %v", err)
	}
}

func TestDecodeReorderedChunkDetectedBySeq(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	_ = Encode(&buf1, Chunk{StreamID: 1, Seq: 0, Total: 2, Payload: []byte("a")})
	_ = Encode(&buf2, Chunk{StreamID: 1, Seq: 1, Total: 2, Payload: []byte("b")})
	// Feed chunk 1 where chunk 0 is expected: the frame decodes cleanly
	// (framing has no ordering guarantee of its own) but reports Seq==1,
	// so a caller enforcing sequential delivery (transfer.go) detects and
	// refuses the reorder itself. This asserts the framing exposes Seq
	// faithfully so that check is possible.
	got, err := Decode(&buf2)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", got.Seq)
	}
}

func TestEncodeRejectsOversizedPayload(t *testing.T) {
	big := make([]byte, maxPayloadLen+1)
	var buf bytes.Buffer
	if err := Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: big}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("oversized payload: want KindInvalidInput, got %v", err)
	}
}

func TestDecodeEmptyReaderReportsNotFound(t *testing.T) {
	if _, err := Decode(bytes.NewReader(nil)); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("empty reader: want KindNotFound, got %v", err)
	}
}

func TestDecodeWrongVersionFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("x")})
	raw := buf.Bytes()
	raw[4] = 99
	if _, err := Decode(bytes.NewReader(raw)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("wrong version: want KindIntegrity, got %v", err)
	}
}

func TestEncodeRejectsSeqAtOrAboveTotal(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, Chunk{StreamID: 1, Seq: 2, Total: 2, Payload: []byte("x")}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("seq >= total: want KindInvalidInput, got %v", err)
	}
}

type erroringWriter struct{}

func (erroringWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestEncodeWriteFailurePropagates(t *testing.T) {
	err := Encode(erroringWriter{}, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("x")})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("write failure: want KindUnavailable, got %v", err)
	}
}

func TestDecodeRejectsForgedOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	_ = Encode(&buf, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("x")})
	raw := buf.Bytes()
	// Forge the payloadLen field to just over the limit without adding
	// bytes: Decode must refuse based on the header alone, before
	// attempting to read (and therefore never allocates) that many bytes.
	raw[29], raw[30], raw[31], raw[32] = 0x01, 0x00, 0x00, 0x01
	if _, err := Decode(bytes.NewReader(raw)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("forged oversized length: want KindInvalidInput, got %v", err)
	}
}

// FuzzSyncChunkDecode is the required real-format fuzz target (06 §5.7):
// Decode must never panic on arbitrary bytes, only return (Chunk{}, err).
// The seed corpus at testdata/fuzz/FuzzSyncChunkDecode/ (native
// "go test fuzz v1" encoding) is auto-loaded by the testing package
// itself; the f.Add calls below add a few more literals inline.
func FuzzSyncChunkDecode(f *testing.F) {
	var validFrame bytes.Buffer
	_ = Encode(&validFrame, Chunk{StreamID: 7, Seq: 0, Total: 1, Payload: []byte("seed payload")})
	f.Add(validFrame.Bytes())
	f.Add([]byte{})
	f.Add([]byte("not a chunk frame at all"))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Decode panicked on input %q: %v", data, r)
			}
		}()
		_, _ = Decode(bytes.NewReader(data))
	})
}
