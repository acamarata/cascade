package minisign

// Purpose: carry the release trust root in the trusted binary.
// Inputs: embedded public file. Outputs: parsed public key or unavailable.
// Constraints: no network trust fetch and no zero-key fallback.
// SPORT: release-signing-custody.
import (
	_ "embed"
	"github.com/acamarata/cascade/pkg/cascade"
)

//go:embed release.pub
var releasePublicKeyBytes []byte

// ReleasePublicKey returns the embedded release trust root, failing closed.
func ReleasePublicKey() (PublicKey, error) {
	k, err := ParsePublicKey(releasePublicKeyBytes)
	if err != nil {
		return PublicKey{}, cascade.New(cascade.KindUnavailable, "minisign: embedded release public key unavailable")
	}
	return k, nil
}
