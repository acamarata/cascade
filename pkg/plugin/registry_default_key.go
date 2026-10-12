package plugin

// Purpose: compile the registry trust root derived from the release seed.
// Inputs: none. Outputs: independent copy of the committed public key.
// Constraints: never contains a private key; override failures never use it.
// SPORT: release-signing-custody.
import "crypto/ed25519"

var registryDefaultPublicKey = [...]byte{
	0xea, 0xb1, 0x26, 0x6e, 0x74, 0xdc, 0xc9, 0x4d,
	0xa1, 0xf1, 0x3a, 0xd9, 0xc0, 0x9e, 0x49, 0x3c,
	0x71, 0x5f, 0xbc, 0x57, 0x21, 0xd9, 0xef, 0x06,
	0x10, 0x0f, 0xc0, 0xd6, 0xaf, 0x89, 0x27, 0xcd,
}

// DefaultRegistryPublicKey returns a copy of the registry public trust root.
func DefaultRegistryPublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), registryDefaultPublicKey[:]...)
}
