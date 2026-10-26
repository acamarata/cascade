package update

// Purpose: the trust root FetchVerified verifies against: the release key
//   embedded in the binary, or the key file CASCADE_MINISIGN_PUBKEY names
//   (the local and test lane).
// Inputs: getenv.
// Outputs: a parsed minisign public key, or a refusal.
// Constraints: no key is ever fetched over the network. Documented
//   residual: CASCADE_MINISIGN_PUBKEY (with CASCADE_UPDATE_BASE_URL and
//   CASCADE_UPDATE_DIST) lets whoever sets the environment choose the trust
//   root and the release source. For an install owned by the invoking user
//   that is no more than that user can already do; never run self-update
//   under sudo -E or any privileged runner that keeps the caller's
//   environment.
// SPORT: internal/update TrustRoot/ADDED.

import (
	"os"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EnvMinisignPubkey names a minisign public key file that replaces the
// embedded release key.
const EnvMinisignPubkey = "CASCADE_MINISIGN_PUBKEY"

// maxPubkeyBytes bounds the key file read.
const maxPubkeyBytes = 4 << 10

// TrustRoot returns the override key when EnvMinisignPubkey is set, else
// minisign.ReleasePublicKey().
func TrustRoot(getenv func(string) string) (minisign.PublicKey, error) {
	path := getenv(EnvMinisignPubkey)
	if path == "" {
		return minisign.ReleasePublicKey()
	}
	f, err := os.Open(path)
	if err != nil {
		return minisign.PublicKey{}, cascade.Wrapf(cascade.KindNotFound, err, "update: read %s=%s", EnvMinisignPubkey, path)
	}
	data, err := readCapped(f, maxPubkeyBytes, EnvMinisignPubkey)
	if err != nil {
		return minisign.PublicKey{}, err
	}
	pub, err := minisign.ParsePublicKey(data)
	if err != nil {
		return minisign.PublicKey{}, cascade.Wrapf(cascade.KindIntegrity, err, "update: %s=%s is not a minisign public key", EnvMinisignPubkey, path)
	}
	return pub, nil
}
