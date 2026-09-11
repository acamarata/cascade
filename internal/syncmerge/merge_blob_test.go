//go:build spike

package syncmerge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

// blobFixtureEntry decodes one entry of a blob-domain fixture side. Either
// DataBase64 (the common case: stage this payload for real and let the
// test derive its declared address from its own real BLAKE3 digest) or the
// FullDataBase64/TruncatedDataBase64 pair (the digest-mismatch case) is
// populated, never both.
type blobFixtureEntry struct {
	LogicalID           string `json:"logicalID"`
	DataBase64          string `json:"dataBase64"`
	FullDataBase64      string `json:"fullDataBase64"`
	TruncatedDataBase64 string `json:"truncatedDataBase64"`
}

func decodeBlobSide(t *testing.T, raw json.RawMessage) []blobFixtureEntry {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var out []blobFixtureEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decodeBlobSide: %v", err)
	}
	return out
}

// stageFromFixture stages a fixture entry's real payload via stageBlob,
// deriving the declared address from the payload's own BLAKE3 digest so
// admission is exercised against real content rather than a hand-authored
// address.
func stageFromFixture(t *testing.T, e blobFixtureEntry) Blob {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(e.DataBase64)
	if err != nil {
		t.Fatalf("stageFromFixture(%s): base64: %v", e.LogicalID, err)
	}
	addr := blake3Hex(data)
	b, err := stageBlob(addr, data)
	if err != nil {
		t.Fatalf("stageFromFixture(%s): unexpected staging error: %v", e.LogicalID, err)
	}
	b.LogicalID = e.LogicalID
	return b
}

func stageSide(t *testing.T, raw json.RawMessage) map[string]Blob {
	t.Helper()
	out := map[string]Blob{}
	for _, e := range decodeBlobSide(t, raw) {
		blob := stageFromFixture(t, e)
		out[blob.Address] = blob
	}
	return out
}

// TestBlobMergeUnion exercises blobMergeUnion and stageBlob against every
// blob-domain adversarial fixture: disjoint sets, identical sets
// (idempotence), the re-chunked-payload two-hash case, and a truncated
// staged blob rejected at admission.
func TestBlobMergeUnion(t *testing.T) {
	t.Run("disjoint-sets", testBlobDisjointSets)
	t.Run("identical-sets-idempotence", testBlobIdenticalSets)
	t.Run("rechunked-two-hashes-survive", testBlobRechunked)
	t.Run("truncated-staged-blob-rejected", testBlobTruncatedRejected)
}

func testBlobDisjointSets(t *testing.T) {
	f := loadFixture(t, "blob-union-disjoint.json")
	a := stageSide(t, f.SideA)
	b := stageSide(t, f.SideB)
	merged := blobMergeUnion(a, b)
	if len(merged) != len(a)+len(b) {
		t.Fatalf("disjoint union size = %d, want %d (no loss)", len(merged), len(a)+len(b))
	}
}

func testBlobIdenticalSets(t *testing.T) {
	f := loadFixture(t, "blob-union-identical.json")
	a := stageSide(t, f.SideA)
	b := stageSide(t, f.SideB)
	merged := blobMergeUnion(a, b)
	if len(merged) != len(a) {
		t.Fatalf("identical-set union size = %d, want %d (idempotence)", len(merged), len(a))
	}
	self := blobMergeUnion(a, a)
	if len(self) != len(a) {
		t.Fatalf("merge(A,A) size = %d, want %d", len(self), len(a))
	}
}

func testBlobRechunked(t *testing.T) {
	f := loadFixture(t, "blob-union-rechunked.json")
	a := stageSide(t, f.SideA)
	b := stageSide(t, f.SideB)
	merged := blobMergeUnion(a, b)
	if len(merged) != 2 {
		t.Fatalf("rechunked union size = %d, want 2 (both distinct hashes must survive, no dedup)", len(merged))
	}
	for addr, blob := range merged {
		if blob.LogicalID != "blob-d" {
			t.Fatalf("merged entry %s has LogicalID %q, want blob-d", addr, blob.LogicalID)
		}
	}
}

func testBlobTruncatedRejected(t *testing.T) {
	f := loadFixture(t, "blob-staging-digest-mismatch.json")
	entries := decodeBlobSide(t, f.SideA)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one fixture entry, got %d", len(entries))
	}
	e := entries[0]
	fullData, err := base64.StdEncoding.DecodeString(e.FullDataBase64)
	if err != nil {
		t.Fatalf("decode full data: %v", err)
	}
	truncatedData, err := base64.StdEncoding.DecodeString(e.TruncatedDataBase64)
	if err != nil {
		t.Fatalf("decode truncated data: %v", err)
	}
	declaredAddress := blake3Hex(fullData)

	// The correct, untruncated transfer must be admitted.
	if _, err := stageBlob(declaredAddress, fullData); err != nil {
		t.Fatalf("full payload should stage cleanly under its own digest: %v", err)
	}
	// The truncated transfer under the SAME declared address must be
	// discarded rather than admitted.
	_, err = stageBlob(declaredAddress, truncatedData)
	if err == nil {
		t.Fatalf("expected ErrBlobDigestMismatch for a truncated transfer, got nil")
	}
	if !errors.Is(err, ErrBlobDigestMismatch) {
		t.Fatalf("stageBlob(truncated) = %v, want ErrBlobDigestMismatch", err)
	}
}
