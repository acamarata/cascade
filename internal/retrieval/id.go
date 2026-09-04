// Purpose: the stable, content-addressed Chunk.ID computation both
// chunkers call (chunk.go's Chunk doc comment). Isolated in its own file
// because Art.7 determinism is the single property every other package
// that consumes internal/retrieval depends on: T2 dedupes upserts by ID,
// T3 dedupes embeddings by ID, and S-11's RRF fusion cites chunks by ID.
//
// Inputs: a chunk's raw Content bytes.
// Outputs: a 64-character lowercase hex BLAKE3-256 digest.
// Constraints: pure function, no I/O; canonicalization normalizes ONLY
// line-ending representation (Art.7's cross-platform stability
// requirement), never other whitespace, so a meaningful whitespace edit
// still produces a new ID (this ticket's own acceptance criterion).
//
// SPORT: internal.retrieval.ChunkID/ADDED (P1-E06-W2-S10-T1).

package retrieval

import (
	"encoding/hex"
	"strings"

	"github.com/zeebo/blake3"
)

// canonicalizeLineEndings normalizes CRLF and lone CR to LF so a chunk
// carved from a CRLF-checked-out file and the identical chunk carved from
// its LF counterpart hash identically (Art.7: determinism must not depend
// on git's autocrlf setting or the platform that checked the tree out).
// No other whitespace is touched: two inputs differing only by a trailing
// space or an extra blank line canonicalize to two different byte strings
// and therefore hash to two different IDs, which is the boundary this
// ticket's whitespace-change test asserts.
func canonicalizeLineEndings(content []byte) []byte {
	s := string(content)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return []byte(s)
}

// ChunkID computes the content-addressed ID for content: the hex-encoded
// BLAKE3-256 digest of its canonicalized form (canonicalizeLineEndings).
// Two calls with byte-identical content, or content differing only by
// line-ending representation, always return the same ID regardless of the
// chunk's Path — this is what lets identical content at two different
// paths dedupe under a single ID (this ticket's other acceptance
// criterion). Uses github.com/zeebo/blake3, the same BLAKE3-256 algorithm
// providers/fs/blobstore.go already standardizes on for this module.
func ChunkID(content []byte) string {
	h := blake3.New()
	_, _ = h.Write(canonicalizeLineEndings(content)) // hash.Hash.Write never errors
	return hex.EncodeToString(h.Sum(nil))
}
