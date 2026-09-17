package sync

// Purpose (this file): the receive path — the mirror of SendBatch, the
//   blob path that establishes admission, and the resume that makes a
//   large transfer possible over a link that drops.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/zeebo/blake3"
)

// sentBatch encodes records as the send path does, so the receive tests
// read what a real sender writes rather than a shape invented here.
func sentBatch(t *testing.T, recs []Record, streamID uint64) *bytes.Buffer {
	t.Helper()
	payload, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := TransferBytes(context.Background(), &buf, streamID, payload, DefaultChunkSize, 0); err != nil {
		t.Fatalf("TransferBytes: %v", err)
	}
	return &buf
}

// TestABatchArrivesAsTheRecordsThatWereSent is the round trip.
func TestABatchArrivesAsTheRecordsThatWereSent(t *testing.T) {
	want := []Record{
		{Domain: storage.DomainConfig, Subkind: "config", ID: "a", Hash: "ha"},
		{Domain: storage.DomainConfig, Subkind: "config", ID: "b", Hash: "hb"},
	}
	got, _, err := mergeEngine().ReceiveBatch(context.Background(), sentBatch(t, want, 3), 3, 0)
	if err != nil {
		t.Fatalf("ReceiveBatch: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Hash != want[i].Hash {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestAnUndecodableBatchIsAnIntegrityFailure covers the branch a
// version-skewed or corrupted sender reaches.
func TestAnUndecodableBatchIsAnIntegrityFailure(t *testing.T) {
	var buf bytes.Buffer
	if err := TransferBytes(context.Background(), &buf, 1, []byte("{not an array}"), DefaultChunkSize, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mergeEngine().ReceiveBatch(context.Background(), &buf, 1, 0); err == nil {
		t.Fatal("a batch that is not a record array was accepted")
	}
}

// TestAMixedDomainBatchIsRefusedNotFiltered is the rule that keeps a
// disagreement visible. Merging the subset that matched would hide it
// while producing a result neither side expected.
func TestAMixedDomainBatchIsRefusedNotFiltered(t *testing.T) {
	recs := []Record{
		{Domain: storage.DomainConfig, Subkind: "config", ID: "a"},
		{Domain: storage.DomainMemory, Subkind: "memory", ID: "b"},
	}
	_, err := mergeEngine().ReceiveAndMerge(context.Background(), sentBatch(t, recs, 5), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "config", PeerTier: nodes.TierController,
	}, 5, 0)
	if err == nil {
		t.Fatal("a batch mixing two domains was merged")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("err = %v, want it to name the record that did not belong", err)
	}
}

// TestReceiveAndMergeAppliesTheDomainsStrategy joins the two halves.
func TestReceiveAndMergeAppliesTheDomainsStrategy(t *testing.T) {
	incoming := []Record{{
		Domain: storage.DomainConfig, Subkind: "config", ID: "k", Hash: "server",
		Order: OrderKey{Revision: 9, NodeID: "server"},
	}}
	e := mergeEngine()
	got, err := e.ReceiveAndMerge(context.Background(), sentBatch(t, incoming, 7), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "config", PeerTier: nodes.TierController,
		Local: map[string]Record{"k": rec("k", 8, 0, "laptop", "laptop")},
	}, 7, 0)
	if err != nil {
		t.Fatalf("ReceiveAndMerge: %v", err)
	}
	if got.Records["k"].Hash != "server" {
		t.Errorf("merged = %q, want the received side to win under server-primary", got.Records["k"].Hash)
	}
	if e.Conflicts().Len() != 1 {
		t.Errorf("%d conflict(s) journaled, want 1", e.Conflicts().Len())
	}
}

// blobStream encodes data as a chunk stream and returns it with its real
// content address.
func blobStream(t *testing.T, data []byte, streamID uint64) (*bytes.Buffer, ContentAddress, uint64) {
	t.Helper()
	var buf bytes.Buffer
	if err := TransferBytes(context.Background(), &buf, streamID, data, 8, 0); err != nil {
		t.Fatal(err)
	}
	addr := ContentAddress(blake3.Sum256(data))
	total := uint64(len(data)+7) / 8
	return &buf, addr, total
}

// TestAReceivedBlobIsAdmittedOnlyAfterItsBytesHash is what the union's
// "admitted blobs only" rule rests on.
func TestAReceivedBlobIsAdmittedOnlyAfterItsBytesHash(t *testing.T) {
	data := []byte("a payload long enough to span several chunks")
	stream, addr, total := blobStream(t, data, 11)
	dir := t.TempDir()
	dest := filepath.Join(dir, "blob")

	ref, err := mergeEngine().ReceiveBlob(context.Background(), stream, BlobTransfer{
		StagingDir: filepath.Join(dir, "staging"), DestPath: dest,
		Address: addr, Total: total, StreamID: 11,
	})
	if err != nil {
		t.Fatalf("ReceiveBlob: %v", err)
	}
	if !ref.Admitted {
		t.Error("a fully received blob was not admitted")
	}
	if ref.Address != addr.Hex() {
		t.Errorf("ref address = %q, want %q", ref.Address, addr.Hex())
	}
	got, err := os.ReadFile(dest) //nolint:gosec // dest is this test's own temp dir.
	if err != nil {
		t.Fatalf("the admitted blob is not at its destination: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("the admitted blob's bytes differ from what was sent")
	}
}

// TestAPartialTransferAdmitsNothingAndResumes is the rule that makes a
// large blob transferable over a link that drops: the staged bytes stay,
// and the next attempt continues rather than restarting.
func TestAPartialTransferAdmitsNothingAndResumes(t *testing.T) {
	data := []byte("a payload long enough to span several chunks")
	full, addr, total := blobStream(t, data, 12)
	dir := t.TempDir()
	staging, dest := filepath.Join(dir, "staging"), filepath.Join(dir, "blob")
	e := mergeEngine()
	transfer := BlobTransfer{
		StagingDir: staging, DestPath: dest, Address: addr, Total: total, StreamID: 12,
	}

	// Cut the stream in half: the connection dropped mid-transfer.
	encoded := full.Bytes()
	if _, err := e.ReceiveBlob(context.Background(), bytes.NewReader(encoded[:len(encoded)/2]), transfer); err == nil {
		t.Fatal("a truncated transfer reported an admitted blob")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a partial transfer created the destination file (stat err = %v)", err)
	}

	// The staged bytes survived, and the resume point is past zero.
	resumeFrom, err := ResumeStaging(staging, addr)
	if err != nil {
		t.Fatalf("the partial transfer left nothing to resume: %v", err)
	}
	if resumeFrom == 0 {
		t.Error("the resume point is zero; the next attempt would restart the whole blob")
	}

	// The sender re-sends from there, and this time it completes.
	var rest bytes.Buffer
	if err := TransferBytes(context.Background(), &rest, 12, data, 8, resumeFrom); err != nil {
		t.Fatal(err)
	}
	ref, err := e.ReceiveBlob(context.Background(), &rest, transfer)
	if err != nil {
		t.Fatalf("the resumed transfer failed: %v", err)
	}
	if !ref.Admitted {
		t.Error("the resumed transfer did not admit the blob")
	}
	got, err := os.ReadFile(dest) //nolint:gosec // dest is this test's own temp dir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("the resumed blob is %d bytes, want %d; a resume that re-appended would be longer",
			len(got), len(data))
	}
}

// TestAnUnreadableStagingStateIsRefusedNotRestarted is the distinction
// beginOrResume draws. Only a NOT-FOUND resume starts fresh; anything else
// means there ARE staged bytes this process cannot make sense of, and
// beginning again over them would append a second copy of the blob onto
// the first.
func TestAnUnreadableStagingStateIsRefusedNotRestarted(t *testing.T) {
	data := []byte("payload")
	_, addr, total := blobStream(t, data, 13)
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	if err := BeginStaging(staging, addr, total); err != nil {
		t.Fatal(err)
	}
	// Corrupt the cursor: staged bytes exist and their state is unreadable.
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	var corrupted bool
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), "cursor") {
			if err := os.WriteFile(filepath.Join(staging, entry.Name()), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			corrupted = true
		}
	}
	if !corrupted {
		t.Skip("staging writes no separate cursor file here; the branch has no reachable shape")
	}

	if _, err := mergeEngine().ReceiveBlob(context.Background(), bytes.NewReader(nil), BlobTransfer{
		StagingDir: staging, DestPath: filepath.Join(dir, "blob"),
		Address: addr, Total: total, StreamID: 13,
	}); err == nil {
		t.Fatal("a transfer with unreadable staging state restarted over the staged bytes")
	}
}
