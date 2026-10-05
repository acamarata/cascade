// Purpose: inspect stale file enrollment without exposing a signer.
// Inputs: data directory. Outputs: public key only.
// Constraints: zero private bytes after reading. SPORT: elevation file detection.

package elevation

import (
	"crypto/ed25519"
	"encoding/base64"
	"github.com/acamarata/cascade/pkg/cascade"
	"os"
	"path/filepath"
	"strings"
)

// TierFile identifies file storage without granting elevation.
const TierFile StorageTier = "file"
const elevationKeyFileName = "elevation.key"

func fileKeyExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, elevationKeyFileName))
	return err == nil
}
func filePublicKey(dir string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(filepath.Join(dir, elevationKeyFileName))
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "elevation: invalid file key")
	}
	defer zero(priv)
	if len(priv) != ed25519.PrivateKeySize {
		return nil, cascade.New(cascade.KindIntegrity, "elevation: invalid file key size")
	}
	derived := ed25519.NewKeyFromSeed(priv[:ed25519.SeedSize])
	defer zero(derived)
	return append(ed25519.PublicKey(nil), derived[ed25519.SeedSize:]...), nil
}
