package generator

// Purpose: separate registry signing from release signing with HKDF.
// Inputs: a copied 32-byte release seed. Outputs: caller-owned private key.
// Constraints: pkg never imports internal; caller erases both seed and result.
// SPORT: release-signing-custody.
import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DeriveRegistryKey derives the registry key without reusing the release key.
func DeriveRegistryKey(releaseSeed []byte) (ed25519.PrivateKey, error) {
	if len(releaseSeed) != ed25519.SeedSize {
		return nil, cascade.New(cascade.KindInvalidInput, "registry-gen: release seed must be 32 bytes")
	}
	seed, err := hkdf.Key(sha256.New, releaseSeed, []byte("cascade"), "cascade/registry-index/v1", ed25519.SeedSize)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "registry-gen: derive registry seed")
	}
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}
