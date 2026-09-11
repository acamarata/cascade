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

func TestSnapshotInitializesRepoOnFirstCall(t *testing.T) {
	ctx := context.Background()
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()

	report, refs, err := Snapshot(ctx, target, recipient, fakeExporter{data: []byte("first snapshot content")})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if report.Chunks != len(refs) {
		t.Fatalf("report.Chunks = %d, want %d (len(refs))", report.Chunks, len(refs))
	}
	cfg, err := ReadRepoConfig(ctx, target)
	if err != nil {
		t.Fatalf("ReadRepoConfig after Snapshot: %v", err)
	}
	if cfg.AgeRecipient != recipient {
		t.Fatalf("repo config recipient = %q, want %q", cfg.AgeRecipient, recipient)
	}
}

func TestSnapshotRefusesRecipientMismatch(t *testing.T) {
	ctx := context.Background()
	_, recipientA := newTestAgeKeypair(t)
	_, recipientB := newTestAgeKeypair(t)
	target := newMemTarget()

	if _, _, err := Snapshot(ctx, target, recipientA, fakeExporter{data: []byte("first")}); err != nil {
		t.Fatalf("Snapshot (first): %v", err)
	}
	_, _, err := Snapshot(ctx, target, recipientB, fakeExporter{data: []byte("second")})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Snapshot(mismatched recipient) error kind = %v, want KindConflict", err)
	}
}

// TestSnapshotSecondCallSameRecipientReusesRepo hits ensureRepoConfig's
// happy-path return-nil branch: a second Snapshot call with the recipient
// the repo was already initialized to must succeed without rewriting the
// config.
func TestSnapshotSecondCallSameRecipientReusesRepo(t *testing.T) {
	ctx := context.Background()
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()

	if _, _, err := Snapshot(ctx, target, recipient, fakeExporter{data: []byte("first")}); err != nil {
		t.Fatalf("Snapshot (first): %v", err)
	}
	if _, _, err := Snapshot(ctx, target, recipient, fakeExporter{data: []byte("second")}); err != nil {
		t.Fatalf("Snapshot (second, same recipient): %v", err)
	}
}

// failingExporter always fails Export, exercising Snapshot's capture-error
// propagation path.
type failingExporter struct{}

func (failingExporter) Export(context.Context) (io.ReadCloser, error) {
	return nil, cascade.New(cascade.KindUnavailable, "failingExporter: always fails")
}

func TestSnapshotPropagatesExportError(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	_, _, err := Snapshot(context.Background(), newMemTarget(), recipient, failingExporter{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Snapshot(failing exporter) error kind = %v, want KindUnavailable", err)
	}
}

// TestSnapshotPropagatesNonNotFoundConfigError hits ensureRepoConfig's
// "read failed for a reason other than absence" branch: a stored config
// document that fails to decode is a different failure than "no repo yet"
// and must propagate rather than being treated as first-use.
func TestSnapshotPropagatesNonNotFoundConfigError(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	if err := target.Put(ctx, "config/repo.json", bytes.NewReader([]byte("not valid json"))); err != nil {
		t.Fatalf("seed malformed config: %v", err)
	}
	_, recipient := newTestAgeKeypair(t)
	_, _, err := Snapshot(ctx, target, recipient, fakeExporter{data: []byte("data")})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Snapshot(malformed stored config) error kind = %v, want KindInvalidInput", err)
	}
}

func TestPipelineWriteRefusesUnparseableRecipient(t *testing.T) {
	p := Pipeline{Target: newMemTarget(), AgeRecipient: "not-an-age-recipient"}
	_, _, err := p.Write(context.Background(), bytes.NewReader([]byte("data")))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Write(unparseable recipient) error kind = %v, want KindInvalidInput", err)
	}
}

func TestPipelineWritePropagatesChunkStreamError(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	p := Pipeline{Target: newMemTarget(), AgeRecipient: recipient}
	_, _, err := p.Write(context.Background(), failingReadCloser{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Write(failing reader) error kind = %v, want KindUnavailable", err)
	}
}

func TestPipelineWritePropagatesDedupError(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	target := newFaultyTarget()
	target.failGet = true
	p := Pipeline{Target: target, AgeRecipient: recipient}
	_, _, err := p.Write(context.Background(), bytes.NewReader([]byte("data")))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Write(faulty target) error kind = %v, want KindUnavailable", err)
	}
}

// TestPipelineReadRefusesHashMismatchWithoutDecryptFailure proves the
// integrity check at readOneChunk's END, not just Decrypt's own tag check:
// an object that decrypts and decompresses CLEANLY but does not match its
// ObjectRef's hash (a chunk substituted for another, both individually
// valid ciphertexts) must still be refused.
func TestPipelineReadRefusesHashMismatchWithoutDecryptFailure(t *testing.T) {
	ctx := context.Background()
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	p := Pipeline{Target: target, AgeRecipient: recipient}

	if _, _, err := p.Write(ctx, bytes.NewReader([]byte("substituted chunk content"))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	compressed, err := Compress([]byte("a completely different chunk"))
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	encrypted, err := Encrypt(recipient, compressed)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	claimedRef := ObjectRef{Hash: ObjectHash([]byte("substituted chunk content"))}
	if err := target.Put(ctx, ObjectKey(claimedRef.Hash), bytes.NewReader(encrypted)); err != nil {
		t.Fatalf("Put substituted object: %v", err)
	}

	got, err := p.Read(ctx, identity, []ObjectRef{claimedRef})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Read(substituted object) error kind = %v, want KindIntegrity", err)
	}
	if got != nil {
		t.Fatalf("Read(substituted object) returned %d bytes, want nil", len(got))
	}
}
