// Package background provides approval digest utilities.
package background

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Approval represents an authorization request with a command, folder, and grant hash.
type Approval struct {
	Command   string
	Folder    string
	GrantHash string
}

// ApprovalDigest computes a SHA-256 digest over the uvarint-length-prefixed
// Command, Folder, and GrantHash fields of the given Approval, returning
// the result as a lowercase hex string.
func ApprovalDigest(a Approval) string {
	var buf []byte
	for _, s := range []string{a.Command, a.Folder, a.GrantHash} {
		tmp := make([]byte, binary.MaxVarintLen64)
		n := binary.PutUvarint(tmp, uint64(len(s)))
		buf = append(buf, tmp[:n]...)
		buf = append(buf, []byte(s)...)
	}
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:])
}

// VerifyApprovalDigest returns true if the provided digest matches the
// computed ApprovalDigest for the given Approval. It returns false for
// any invalid or mismatched digest without producing an error.
func VerifyApprovalDigest(a Approval, digest string) bool {
	return ApprovalDigest(a) == digest
}
