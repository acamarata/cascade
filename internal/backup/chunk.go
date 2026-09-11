// Purpose: content-defined chunking of a captured domain stream
//
//	(01-FEATURE-INVENTORY's "restic-style" row): cut points are a
//	deterministic function of the bytes seen so far, so an insertion or
//	deletion anywhere in a stream re-cuts only the chunks touching the
//	edit — everything else re-hashes identically, which is what lets
//	Dedup skip unchanged content across snapshots.
//
// Inputs: an io.Reader or []byte holding one capture's plaintext bytes.
// Outputs: an ordered []Chunk covering the input with no gaps and no
//
//	overlap.
//
// Constraints: Art.7 determinism — the cut algorithm reads no clock and no
//
//	randomness; two calls over the same bytes always produce the same
//	chunk boundaries.
//
// SPORT: internal.backup.chunk/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// chunkMinSize floors a cut point so pathological input (e.g. a run
	// of identical bytes) cannot produce a stream of one-byte chunks.
	chunkMinSize = 1 << 12 // 4 KiB
	// chunkMaxSize forces a cut even if the rolling hash never signals
	// one, bounding worst-case chunk size and worst-case memory per
	// chunk.
	chunkMaxSize = 1 << 16 // 64 KiB
	// chunkMask sets the target average chunk size: a cut fires when the
	// low bits of the rolling hash are all zero, which happens with
	// probability 1/(chunkMask+1) at each byte once past chunkMinSize.
	chunkMask = 1<<13 - 1 // ~8 KiB average
	// chunkMul is a fixed odd 64-bit constant (2^64/phi, the golden-ratio
	// multiplier) used to mix each byte into the rolling hash. It is a
	// compile-time constant, never math/rand: the same bytes always
	// produce the same hash sequence.
	chunkMul = 0x9E3779B97F4A7C15
)

// Chunk is one content-defined slice of a capture stream, in stream order.
// Data aliases the caller's backing array; callers that need to retain a
// Chunk past the next mutation of that array must copy it themselves.
type Chunk struct {
	Data []byte
}

// ChunkStream reads r fully and splits it into content-defined chunks.
// Empty input yields a nil, empty slice with no error.
func ChunkStream(r io.Reader) ([]Chunk, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: read capture stream for chunking")
	}
	return ChunkBytes(data), nil
}

// ChunkBytes is ChunkStream's pure core: chunk boundaries are a
// deterministic function of data's bytes alone. Splitting it out lets
// pipeline_test.go and chunk_test.go assert on chunk boundaries directly,
// without an io.Reader indirection.
func ChunkBytes(data []byte) []Chunk {
	if len(data) == 0 {
		return nil
	}
	var chunks []Chunk
	start := 0
	var h uint64
	for i, b := range data {
		h = h*chunkMul + uint64(b) + 1
		size := i - start + 1
		atCut := size >= chunkMinSize && h&chunkMask == 0
		if atCut || size >= chunkMaxSize {
			chunks = append(chunks, Chunk{Data: data[start : i+1]})
			start = i + 1
			h = 0
		}
	}
	if start < len(data) {
		chunks = append(chunks, Chunk{Data: data[start:]})
	}
	return chunks
}
