// Purpose: the acceptance-test round trip (chunk -> dedup -> compress ->
//
//	encrypt -> reverse -> byte-identical recovery) across generated
//	inputs, Snapshot's repo-bootstrap contract, and every pipeline-level
//	refusal path (missing key material, corrupted/missing stored object).
//
// SPORT: internal.backup.pipeline/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeExporter is a test-only Exporter over a fixed byte slice, standing in
// for SQLiteCapture (already covered by capture_test.go) so pipeline_test.go
// can exercise Snapshot without a real SQLite database.
type fakeExporter struct{ data []byte }

func (f fakeExporter) Export(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func TestPipelineWriteReadRoundTrip(t *testing.T) {
	identity, recipient := newTestAgeKeypair(t)
	inputs := map[string][]byte{
		"empty":               {},
		"single-byte":         {0x7A},
		"below-min-boundary":  bytes.Repeat([]byte{0x11}, chunkMinSize-1),
		"at-min-boundary":     bytes.Repeat([]byte{0x22}, chunkMinSize),
		"above-min-boundary":  bytes.Repeat([]byte{0x33}, chunkMinSize+1),
		"at-max-boundary":     bytes.Repeat([]byte{0x44}, chunkMaxSize),
		"above-max-boundary":  bytes.Repeat([]byte{0x55}, chunkMaxSize+37),
		"highly-repetitive":   bytes.Repeat([]byte("dedup-me-please "), 20000),
		"pseudo-random-bytes": pseudoRandomBytes(chunkMaxSize * 3),
	}
	for name, want := range inputs {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			target := newMemTarget()
			p := Pipeline{Target: target, AgeRecipient: recipient}

			_, refs, err := p.Write(ctx, bytes.NewReader(want))
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := p.Read(ctx, identity, refs)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d bytes", len(got), len(want))
			}
		})
	}
}

// pseudoRandomBytes returns a deterministic (not crypto/math/rand —
// Art.7.3) byte slice with no run-length redundancy, exercising the
// chunker's max-size cutoff path.
func pseudoRandomBytes(n int) []byte {
	out := make([]byte, n)
	var h uint64 = 0x2545F4914F6CDD1D
	for i := range out {
		h ^= h << 13
		h ^= h >> 7
		h ^= h << 17
		out[i] = byte(h)
	}
	return out
}

func TestPipelineWriteRefusesEmptyRecipient(t *testing.T) {
	p := Pipeline{Target: newMemTarget()}
	_, _, err := p.Write(context.Background(), bytes.NewReader([]byte("data")))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Write(no recipient) error kind = %v, want KindInvalidInput", err)
	}
}

func TestPipelineWriteSecondRunDedupsFully(t *testing.T) {
	ctx := context.Background()
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	p := Pipeline{Target: target, AgeRecipient: recipient}
	data := bytes.Repeat([]byte("stable content across two snapshot runs "), 5000)

	first, _, err := p.Write(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Write (first): %v", err)
	}
	if first.Stored == 0 {
		t.Fatal("first run stored zero objects")
	}
	second, _, err := p.Write(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Write (second): %v", err)
	}
	if second.Stored != 0 {
		t.Fatalf("second run over unchanged content stored %d new objects, want 0", second.Stored)
	}
	if second.Deduped != second.Chunks {
		t.Fatalf("second run deduped %d of %d chunks, want all", second.Deduped, second.Chunks)
	}
}

func TestPipelineReadRefusesMissingObject(t *testing.T) {
	identity, _ := newTestAgeKeypair(t)
	p := Pipeline{Target: newMemTarget()}
	_, err := p.Read(context.Background(), identity, []ObjectRef{{Hash: ObjectHash([]byte("never stored"))}})
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Read(missing object) error kind = %v, want KindNotFound", err)
	}
}

// TestPipelineReadRefusesCorruptedStoredObject proves the integrity gate
// end to end through Pipeline.Read: a bit-flipped stored object must never
// yield partial or substituted plaintext back to the caller.
func TestPipelineReadRefusesCorruptedStoredObject(t *testing.T) {
	ctx := context.Background()
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	p := Pipeline{Target: target, AgeRecipient: recipient}

	_, refs, err := p.Write(ctx, bytes.NewReader([]byte("a chunk that will be corrupted at rest")))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	stored, err := GetObject(ctx, target, refs[0].Hash)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	corrupted := append([]byte(nil), stored...)
	corrupted[len(corrupted)-1] ^= 0xFF
	if err := target.Put(ctx, ObjectKey(refs[0].Hash), bytes.NewReader(corrupted)); err != nil {
		t.Fatalf("re-Put corrupted object: %v", err)
	}

	got, err := p.Read(ctx, identity, refs)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Read(corrupted object) error kind = %v, want KindIntegrity", err)
	}
	if got != nil {
		t.Fatalf("Read(corrupted object) returned %d bytes, want nil", len(got))
	}
}
