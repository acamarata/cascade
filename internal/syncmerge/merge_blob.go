//go:build spike

package syncmerge

import (
	"encoding/hex"
	"fmt"

	"github.com/zeebo/blake3"
)

// Blob is one content-addressed blob (R-21.223 domain 4). Address is the
// hex BLAKE3 digest the blob is declared under. LogicalID groups blobs that
// represent the same logical payload under different chunkings (the
// re-chunked-payload adversarial case): two Blobs with the same LogicalID
// but different Address values both survive the union, by design (see
// docs/adrs/ADR-sync-merge-semantics.md's re-chunked-blob constraint).
type Blob struct {
	Address     string
	LogicalID   string
	Data        []byte
	Sensitivity Sensitivity
}

func (b Blob) recordID() string             { return b.Address }
func (b Blob) sensitivityTier() Sensitivity { return b.Sensitivity }

// blake3Hex returns the hex-encoded BLAKE3 digest of data.
func blake3Hex(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stageBlob admits a staged blob only when the recomputed BLAKE3 digest of
// data equals declaredAddress (R-21.223 domain 4). A mismatch -- including
// a truncated transfer, which changes the digest -- discards the staged
// bytes and returns ErrBlobDigestMismatch; it never commits under a
// wrong-looking address.
func stageBlob(declaredAddress string, data []byte) (Blob, error) {
	got := blake3Hex(data)
	if got != declaredAddress {
		return Blob{}, fmt.Errorf("%w: declared=%s recomputed=%s", ErrBlobDigestMismatch, declaredAddress, got)
	}
	return Blob{Address: declaredAddress, Data: data}, nil
}

// blobMergeUnion unions two sides of the blob domain, keyed by content
// address, over ADMITTED blobs only. Because the key is itself the content
// digest, union is trivially commutative, associative and idempotent: two
// blobs sharing an address are byte-identical by definition of the digest,
// so there is never a real conflict to resolve, only presence or absence.
func blobMergeUnion(sideA, sideB map[string]Blob) map[string]Blob {
	merged := make(map[string]Blob, len(sideA)+len(sideB))
	for addr, b := range sideA {
		merged[addr] = b
	}
	for addr, b := range sideB {
		merged[addr] = b
	}
	return merged
}
