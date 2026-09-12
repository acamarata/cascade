// Purpose: the chunked transfer loop (S-38.T1 owns the framing and this
//   loop; S-36.T3's tunnel + its forwarded node RPC channel is the
//   transport this rides — NO new dialer, NO new ssh stack, NO new
//   crypto: SendStream/ReceiveStream take a plain io.Writer/io.Reader,
//   exactly the shape of internal/nodes.Conn (io.ReadWriteCloser), so a
//   production caller hands this the tunnel's already-established
//   forwarded channel directly).
// Inputs: a stream id, the declared total chunk count, a resume point
//   (fromSeq / the receive-side's already-durable chunk count), and a
//   ChunkSource/ChunkSink the caller supplies (an in-memory record batch
//   for TransferBytes, or staging.go's AppendStagedChunk for a blob).
// Outputs: the sender reports the first error it hits (or nil); the
//   receiver additionally reports how many chunks it durably sunk before
//   stopping, so a caller can resume from exactly that point rather than
//   guessing.
// Constraints: RECEIVE IS STRICTLY SEQUENTIAL — a chunk whose Seq does
//   not equal the next expected index is refused as KindIntegrity,
//   whether it is reordered, a duplicate, or from the wrong stream; this
//   is what makes "tunnel drop mid-transfer, resume from last-acked
//   cursor" possible without a reassembly buffer keyed by seq. A
//   canceled ctx aborts before touching the next chunk (KindCanceled).
// SPORT: internal.sync.transfer/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"bytes"
	"context"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultChunkSize is the plaintext payload size TransferBytes/TransferBlob
// split at absent an explicit override.
const DefaultChunkSize = 64 << 10 // 64 KiB

// ChunkSource supplies the plaintext bytes for chunk seq of a stream whose
// total chunk count is already fixed.
type ChunkSource func(seq uint64) ([]byte, error)

// ChunkSink receives one newly-verified, in-order chunk. Returning an
// error stops the receive loop at that chunk (the chunk itself is NOT
// re-delivered — a caller whose sink failed for a transient reason of its
// own must resume from this same seq, not seq+1).
type ChunkSink func(seq, total uint64, payload []byte) error

// SendStream writes chunks [fromSeq, total) of streamID to conn, in
// order, sourcing each from source. It returns the first error hit,
// leaving the caller free to retry a later SendStream call with fromSeq
// advanced to whatever the receiver last acknowledged.
func SendStream(ctx context.Context, conn io.Writer, streamID, total, fromSeq uint64, source ChunkSource) error {
	if fromSeq > total {
		return cascade.Newf(cascade.KindInvalidInput, "sync: resume point %d exceeds total %d", fromSeq, total)
	}
	for seq := fromSeq; seq < total; seq++ {
		if err := ctx.Err(); err != nil {
			return cascade.Wrap(cascade.KindCanceled, err, "sync: transfer canceled")
		}
		payload, err := source(seq)
		if err != nil {
			return err
		}
		if err := Encode(conn, Chunk{StreamID: streamID, Seq: seq, Total: total, Payload: payload}); err != nil {
			return err
		}
	}
	return nil
}

// ReceiveStream reads chunks from conn starting at the expectation that
// the next chunk to arrive has Seq == fromSeq (0 for a fresh transfer,
// or the count already durably sunk on a resumed attempt). It stops at
// the stream's own declared Total or the first error, calling sink once
// per verified chunk in strict order. received reports how many chunks
// (counting from fromSeq) were durably sunk before stopping — the
// caller's next resume point on any non-nil err.
func ReceiveStream(ctx context.Context, conn io.Reader, streamID, fromSeq uint64, sink ChunkSink) (received uint64, err error) {
	expect := fromSeq
	for {
		if cErr := ctx.Err(); cErr != nil {
			return expect, cascade.Wrap(cascade.KindCanceled, cErr, "sync: transfer canceled")
		}
		c, derr := Decode(conn)
		if derr != nil {
			return expect, derr
		}
		if c.StreamID != streamID {
			return expect, cascade.Newf(cascade.KindIntegrity, "sync: chunk stream id %d does not match expected %d", c.StreamID, streamID)
		}
		if c.Seq != expect {
			return expect, cascade.Newf(cascade.KindIntegrity, "sync: out-of-order chunk: got seq %d, expected %d (reordered, duplicate, or gapped)", c.Seq, expect)
		}
		if sinkErr := sink(c.Seq, c.Total, c.Payload); sinkErr != nil {
			return expect, sinkErr
		}
		expect++
		if expect >= c.Total {
			return expect, nil
		}
	}
}

// chunkCount is the ceiling division used to derive a stream's fixed
// Total from a payload length and a chunk size.
func chunkCount(payloadLen, chunkSize int) uint64 {
	if payloadLen == 0 {
		return 1 // an empty payload still transfers as one zero-length chunk
	}
	n := (payloadLen + chunkSize - 1) / chunkSize
	return uint64(n)
}

// TransferBytes sends payload as a chunked stream, splitting it at
// chunkSize (DefaultChunkSize if <= 0), resuming from fromSeq.
func TransferBytes(ctx context.Context, conn io.Writer, streamID uint64, payload []byte, chunkSize int, fromSeq uint64) error {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	total := chunkCount(len(payload), chunkSize)
	source := func(seq uint64) ([]byte, error) {
		start := int(seq) * chunkSize
		if start > len(payload) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "sync: chunk seq %d starts past payload end", seq)
		}
		end := start + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		return payload[start:end], nil
	}
	return SendStream(ctx, conn, streamID, total, fromSeq, source)
}

// ReceiveBytes reassembles the chunks received in THIS call (starting at
// fromSeq) into a single in-memory buffer, for small payloads such as a
// filtered record batch. On a resumed call (fromSeq > 0) the caller
// appends this return value to whatever it already reassembled from the
// earlier attempt — ReceiveBytes never re-fetches or re-returns bytes for
// chunks before fromSeq. Blob transfers use ReceiveStream directly with
// staging.go's ChunkSink so bytes never accumulate fully in memory.
func ReceiveBytes(ctx context.Context, conn io.Reader, streamID, fromSeq uint64) ([]byte, uint64, error) {
	var buf bytes.Buffer
	received, err := ReceiveStream(ctx, conn, streamID, fromSeq, func(_, _ uint64, payload []byte) error {
		_, werr := buf.Write(payload)
		return werr
	})
	return buf.Bytes(), received, err
}
