// Package minisign is the one minisign codec in Cascade: a parser and
// verifier for minisign's armored detached-signature file and its
// two-line public-key file, moved unchanged from internal/nodes.
//
// Formats: the four-line signature file (untrusted comment, base64
// signature data, trusted comment, base64 global signature) and the
// two-line public-key file, with the legacy "Ed" and the prehashed "ED"
// algorithms. Verify returns nil only when both the message signature and
// the comment-binding global signature verify; every refusal fails closed.
//
// Provenance: fixtures and the real-CLI proof are recorded in
// testdata/README.md. No other package parses minisign data
// (TestNoSecondMinisignCodec enforces it).
//
// Imports are limited to the standard library, golang.org/x/crypto/blake2b
// and pkg/cascade.
package minisign
