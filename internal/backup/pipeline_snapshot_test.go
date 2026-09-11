// Purpose: Snapshot's repo-bootstrap contract and the remaining
//
//	pipeline-level refusal paths that did not fit pipeline_test.go under
//	the 300-line cap (mechanical split, no behavior difference from a
//	single file).
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
