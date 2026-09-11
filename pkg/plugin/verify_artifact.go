package plugin

// Purpose: the standalone checksum-verification helper X/S-50.T1 and its
//   downstream consumer X/S-50.T8 both call: checks a fetched artifact's
//   bytes against the checksum published in its RegistryVersionEntry,
//   independent of any RegistryVerifier construction (06 §5.1).
// Inputs: a RegistryVersionEntry (for its hex-encoded Checksum field) and
//   the artifact's raw bytes.
// Outputs: nil on match; ErrChecksumMismatch (KindIntegrity) on any
//   mismatch, missing checksum, or malformed hex.
// Constraints: constant-time comparison (crypto/subtle) so a checksum
//   check can never leak timing information about how much of a forged
//   digest matched.
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/acamarata/cascade/pkg/cascade"
)

// VerifyArtifact checks that the SHA-256 hex digest of artifactData
// matches entry.Checksum. An empty or non-hex Checksum is refused rather
// than treated as "no checksum to check" — a registry entry with no
// checksum can never be trusted (fail closed).
func VerifyArtifact(entry RegistryVersionEntry, artifactData []byte) error {
	if entry.Checksum == "" {
		return cascade.New(cascade.KindIntegrity, ErrChecksumMismatch.Msg+": entry has no checksum")
	}
	want, err := hex.DecodeString(entry.Checksum)
	if err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, err, "%s: entry checksum is not valid hex", ErrChecksumMismatch.Msg)
	}
	got := sha256.Sum256(artifactData)
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return cascade.New(cascade.KindIntegrity, ErrChecksumMismatch.Msg+": artifact checksum mismatch")
	}
	return nil
}
