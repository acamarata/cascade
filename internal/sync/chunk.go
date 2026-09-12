// Purpose: the chunked-transfer wire framing (S-36.T3's contract names
//   this ticket the owner of "chunked-transfer framing"): encode/decode
//   of one chunk frame, fail-closed on truncated/malformed/wrong-size
//   input. This is the decoder FuzzSyncChunkDecode exercises.
// Inputs: Encode takes a Chunk; Decode takes an io.Reader positioned at a
//   frame boundary.
// Outputs: Decode returns a Chunk or a *cascade.Error (KindIntegrity for
//   a corrupt/malformed frame, KindInvalidInput for a declared length
//   this build refuses to allocate for).
// Constraints: PayloadHash is a TRANSPORT-layer corruption check computed
//   by both ends from the payload bytes actually on the wire — it is NOT
//   the content-address trust boundary (staging.go's BLAKE3 admission is)
//   and never substitutes for it: a chunk that decodes cleanly still
//   passes through the caller's own record/blob integrity checks before
//   anything is admitted. maxPayloadLen bounds allocation BEFORE any
//   payload bytes are read, so a forged length header cannot allocate an
//   unbounded buffer ahead of the truncation check.
// SPORT: internal.sync.chunk/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// chunkMagic identifies a well-formed frame start.
var chunkMagic = [4]byte{'C', 'S', 'Y', 'N'}

const chunkVersion = 1

// maxPayloadLen bounds one chunk's payload: large enough for any
// reasonable chunk size this engine chooses, small enough that a forged
// length header cannot be used to force a multi-gigabyte allocation
// before the frame is even validated.
const maxPayloadLen = 16 << 20 // 16 MiB

// headerLen is the fixed-size portion preceding the payload: magic(4) +
// version(1) + streamID(8) + seq(8) + total(8) + payloadLen(4) +
// payloadHash(32).
const headerLen = 4 + 1 + 8 + 8 + 8 + 4 + 32

// Chunk is one framed unit of a chunked transfer.
type Chunk struct {
	// StreamID correlates every chunk of one logical transfer.
	StreamID uint64
	// Seq is this chunk's zero-based index within the stream.
	Seq uint64
	// Total is the stream's total chunk count, known up front — an
	// unknown/streaming total is not supported: resumability requires
	// knowing when the last chunk has arrived.
	Total uint64
	// Payload is this chunk's plaintext bytes.
	Payload []byte
}

// payloadHash returns the transport-layer corruption check for payload:
// blake3 over the bytes exactly as they ride the wire.
func payloadHash(payload []byte) [32]byte {
	return blake3.Sum256(payload)
}

// Encode writes c's wire frame to w.
func Encode(w io.Writer, c Chunk) error {
	if len(c.Payload) > maxPayloadLen {
		return cascade.Newf(cascade.KindInvalidInput, "sync: chunk payload %d bytes exceeds the %d-byte frame limit", len(c.Payload), maxPayloadLen)
	}
	if c.Total == 0 || c.Seq >= c.Total {
		return cascade.Newf(cascade.KindInvalidInput, "sync: chunk seq %d is not within total %d", c.Seq, c.Total)
	}
	hash := payloadHash(c.Payload)
	buf := make([]byte, 0, headerLen+len(c.Payload))
	buf = append(buf, chunkMagic[:]...)
	buf = append(buf, chunkVersion)
	buf = binary.BigEndian.AppendUint64(buf, c.StreamID)
	buf = binary.BigEndian.AppendUint64(buf, c.Seq)
	buf = binary.BigEndian.AppendUint64(buf, c.Total)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(c.Payload)))
	buf = append(buf, hash[:]...)
	buf = append(buf, c.Payload...)
	_, err := w.Write(buf)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: chunk frame write failed")
	}
	return nil
}

// Decode reads and validates one chunk frame from r. It fails closed on
// every malformed shape: wrong magic/version, a declared length exceeding
// maxPayloadLen, a truncated header or payload (io.ErrUnexpectedEOF wraps
// as KindIntegrity — a frame that stops mid-write is corrupt, not simply
// absent), and a payload whose recomputed hash mismatches the frame's own
// declared hash.
func Decode(r io.Reader) (Chunk, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF {
			return Chunk{}, cascade.Wrap(cascade.KindNotFound, err, "sync: no chunk frame available")
		}
		return Chunk{}, cascade.Wrap(cascade.KindIntegrity, err, "sync: truncated chunk header")
	}
	if !bytes.Equal(header[0:4], chunkMagic[:]) {
		return Chunk{}, cascade.New(cascade.KindIntegrity, "sync: chunk frame has the wrong magic bytes")
	}
	if header[4] != chunkVersion {
		return Chunk{}, cascade.Newf(cascade.KindIntegrity, "sync: chunk frame version %d unsupported", header[4])
	}
	streamID := binary.BigEndian.Uint64(header[5:13])
	seq := binary.BigEndian.Uint64(header[13:21])
	total := binary.BigEndian.Uint64(header[21:29])
	payloadLen := binary.BigEndian.Uint32(header[29:33])
	declaredHash := header[33:65]

	if total == 0 || seq >= total {
		return Chunk{}, cascade.Newf(cascade.KindIntegrity, "sync: chunk seq %d is not within total %d", seq, total)
	}
	if payloadLen > maxPayloadLen {
		return Chunk{}, cascade.Newf(cascade.KindInvalidInput, "sync: declared payload length %d exceeds the %d-byte frame limit", payloadLen, maxPayloadLen)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Chunk{}, cascade.Wrap(cascade.KindIntegrity, err, "sync: truncated chunk payload")
	}
	gotHash := payloadHash(payload)
	if !bytes.Equal(gotHash[:], declaredHash) {
		return Chunk{}, cascade.New(cascade.KindIntegrity, "sync: chunk payload hash mismatch")
	}
	return Chunk{StreamID: streamID, Seq: seq, Total: total, Payload: payload}, nil
}
