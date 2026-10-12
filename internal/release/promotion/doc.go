// Package promotion is the release promotion machinery: the promotion
// record (contract:promotion-record), the signed-set verifier that is the
// promote job's trust root, and the byte-identity verifier that compares the
// staging rc artifact set with the public release set.
//
// Purpose: prove that the bytes a release publishes are exactly the bytes
// the offline release key signed at the rc cut, and leave an audit record
// of them.
// Inputs: a dist-<id> directory (docs/developer/release-train.md, "The
// local signing step"), a minisign public key, tags, a commit and the
// release-gate report path.
// Outputs: Record values, Reports listing the files that were checked, and
// cascade-kinded errors that name the file or check that refused.
// Constraints: every digest compared is computed from bytes this package
// read itself, never taken from metadata; the minisign signature over
// cascade_checksums.txt is verified before any line of it is trusted; the
// record is an audit artifact and never a digest source for publishing.
// SPORT: internal/release/promotion (ADD).
package promotion
