// Purpose: ChunkBytes/ChunkStream determinism, boundary sizes, and the
//
//	no-gaps-no-overlap reassembly invariant every later pipeline test
//	relies on.
//
// SPORT: internal.backup.chunk/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"bytes"
	"testing"
)

// reassemble concatenates chunks back into one slice, the invariant every
// caller of ChunkBytes depends on: chunks cover the input with no gaps and
// no overlap.
func reassemble(chunks []Chunk) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c.Data...)
	}
	return out
}

func TestChunkBytesEmptyInput(t *testing.T) {
	if got := ChunkBytes(nil); got != nil {
		t.Fatalf("ChunkBytes(nil) = %v chunks, want nil", got)
	}
}

func TestChunkBytesSingleByte(t *testing.T) {
	chunks := ChunkBytes([]byte{0x42})
	if !bytes.Equal(reassemble(chunks), []byte{0x42}) {
		t.Fatalf("reassemble mismatch for single-byte input")
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestChunkBytesReassemblesExactly(t *testing.T) {
	sizes := []int{0, 1, chunkMinSize - 1, chunkMinSize, chunkMinSize + 1, chunkMaxSize, chunkMaxSize + 1, chunkMaxSize*3 + 17}
	for _, n := range sizes {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i % 251)
		}
		chunks := ChunkBytes(data)
		if !bytes.Equal(reassemble(chunks), data) {
			t.Fatalf("reassemble mismatch for %d-byte input", n)
		}
		for _, c := range chunks {
			if len(c.Data) > chunkMaxSize {
				t.Fatalf("chunk of %d bytes exceeds chunkMaxSize %d", len(c.Data), chunkMaxSize)
			}
		}
	}
}

func TestChunkBytesDeterministic(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog "), 500)
	a := ChunkBytes(data)
	b := ChunkBytes(data)
	if len(a) != len(b) {
		t.Fatalf("chunk count differs across identical calls: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("chunk %d differs across identical calls", i)
		}
	}
}

// TestChunkBytesRepetitiveInputSharesBoundaries proves the property Dedup
// depends on: appending unchanged repetitive content produces a shared
// PREFIX of identical chunks, not a totally different cut pattern.
func TestChunkBytesRepetitiveInputSharesBoundaries(t *testing.T) {
	base := bytes.Repeat([]byte("cascade-backup-content-defined-chunking "), 2000)
	extended := append(append([]byte(nil), base...), []byte("-appended-tail")...)

	a := ChunkBytes(base)
	b := ChunkBytes(extended)
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected at least one chunk from repetitive input")
	}
	shared := 0
	for shared < len(a) && shared < len(b)-1 && bytes.Equal(a[shared].Data, b[shared].Data) {
		shared++
	}
	if shared == 0 {
		t.Fatal("appending content changed every chunk boundary; content-defined chunking produced no shared prefix")
	}
}

func TestChunkStreamMatchesChunkBytes(t *testing.T) {
	data := bytes.Repeat([]byte("stream vs bytes "), 1000)
	viaStream, err := ChunkStream(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ChunkStream: %v", err)
	}
	viaBytes := ChunkBytes(data)
	if !bytes.Equal(reassemble(viaStream), reassemble(viaBytes)) {
		t.Fatal("ChunkStream and ChunkBytes disagree on the same input")
	}
}

func TestChunkStreamPropagatesReadError(t *testing.T) {
	if _, err := ChunkStream(failingReadCloser{}); err == nil {
		t.Fatal("ChunkStream(failing reader) returned nil error")
	}
}
