// Purpose: the content-addressed dedup store — object id = blake3 hash of
//
//	the PRE-ENCRYPTION chunk payload, computed BEFORE zstd/age run
//	(hashing is deterministic; age encryption is randomized per call, so
//	hashing the ciphertext would defeat dedup entirely — see Pipeline's
//	doc comment for the tradeoff this preserves).
//
// Inputs: parallel hashes/payloads slices (payloads already
//
//	compressed+encrypted by the caller — Dedup performs no transform of
//	its own).
//
// Outputs: one DedupResult per input chunk, reporting whether the object
//
//	was newly stored or already present.
//
// Constraints: "only new/changed chunks upload" — Dedup never re-writes an
//
//	object whose id is already present in t.
//
// SPORT: internal.backup.dedup/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"context"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ObjectHash returns the blake3 hash of payload. Called on the PLAINTEXT
// chunk, before Compress or Encrypt run, so identical plaintext content
// across snapshots always hashes to the same id even though the encrypted
// bytes that eventually get stored differ on every call (Encrypt's doc
// comment).
func ObjectHash(payload []byte) [32]byte {
	return blake3.Sum256(payload)
}

// DedupResult reports one chunk's outcome against the object store.
type DedupResult struct {
	Hash  [32]byte
	Bytes int
	// New is false when the object id was already present and the store
	// was skipped.
	New bool
}

// Dedup stores each chunk's already-transformed payload (payloads[i], the
// compressed+encrypted bytes) under the hash of its PRE-TRANSFORM plaintext
// (hashes[i]), skipping any chunk whose object id is already present in t.
// len(hashes) must equal len(payloads).
func Dedup(ctx context.Context, t Target, hashes [][32]byte, payloads [][]byte) ([]DedupResult, error) {
	if len(hashes) != len(payloads) {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: dedup hash/payload count mismatch")
	}
	results := make([]DedupResult, len(hashes))
	for i, h := range hashes {
		existed, err := PutObject(ctx, t, h, payloads[i])
		if err != nil {
			return nil, err
		}
		results[i] = DedupResult{Hash: h, Bytes: len(payloads[i]), New: !existed}
	}
	return results, nil
}
