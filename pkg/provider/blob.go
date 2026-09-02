// Purpose: declare the BlobStore family contract — content-addressed binary
//   storage keyed by a BLAKE3 digest, per B/S-02.T1's task text.
// Inputs: a namespace and raw content (Put) or a Hash (Get/Delete/Exists).
// Outputs: the content's Hash (Put) or its bytes (Get), or a pkg/cascade
//   taxonomy error.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); this
//   package declares the Hash TYPE only — it does not depend on a BLAKE3
//   library (no new module dependency is authorized on this ticket per
//   R-14.115). Computing the digest is each concrete driver's job (the fs
//   driver lands in B/S-02.T2 per R-14.6); this interface only fixes the
//   digest's shape and the operations content-addressing requires.
// SPORT: pkg.provider.BlobStore/ADDED (P1-E02-W1-S02-T1).

package provider

import (
	"context"
	"io"
)

// HashSize is the digest length in bytes for a BLAKE3-256 hash, the
// algorithm this family's content addressing uses.
const HashSize = 32

// Hash is a content digest identifying one blob. It is the BLAKE3-256 hash
// of the blob's bytes; two blobs with identical content always share a
// Hash, and BlobStore.Put returns the Hash so callers can address the blob
// again without recomputing it themselves.
type Hash [HashSize]byte

// IsZero reports whether h is the zero Hash (no content hashed into it). A
// zero Hash is never a valid blob address.
func (h Hash) IsZero() bool {
	return h == Hash{}
}

// String returns h's lowercase hex encoding, the form used in logs,
// filenames, and diagnostics.
func (h Hash) String() string {
	const hexDigits = "0123456789abcdef"
	buf := make([]byte, HashSize*2)
	for i, b := range h {
		buf[i*2] = hexDigits[b>>4]
		buf[i*2+1] = hexDigits[b&0x0f]
	}
	return string(buf)
}

// BlobStore is the content-addressed binary storage contract: a blob's
// identity is the BLAKE3-256 digest of its own bytes, so Put is idempotent
// (storing the same content twice yields the same Hash and no duplicate
// storage) and every read/delete addresses content by that digest rather
// than by a caller-chosen key.
type BlobStore interface {
	// Put streams data into namespace and returns the resulting content
	// Hash. If a blob with that Hash already exists in namespace, Put MUST
	// still succeed (idempotent) without erroring or requiring the data be
	// re-read past what identity verification needs.
	Put(ctx context.Context, namespace string, data io.Reader) (Hash, error)

	// Get streams the blob addressed by hash back to the caller. Get
	// returns a cascade.KindNotFound error if no blob with that Hash
	// exists in namespace. The caller MUST Close the returned
	// io.ReadCloser.
	Get(ctx context.Context, namespace string, hash Hash) (io.ReadCloser, error)

	// Delete removes the blob addressed by hash from namespace. Deleting
	// an absent Hash is not an error.
	Delete(ctx context.Context, namespace string, hash Hash) error

	// Exists reports whether a blob addressed by hash is present in
	// namespace, without transferring its content.
	Exists(ctx context.Context, namespace string, hash Hash) (bool, error)
}
