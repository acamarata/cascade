package promotion

import (
	"bytes"
	"crypto/ed25519"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/minisign"
)

// readSetFile reads a fixed-name file of a dist-<id> set, refusing (naming
// it) when it is absent or unreadable.
func readSetFile(dir, name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return nil, refused("%s is missing or unreadable", name)
	}
	return b, nil
}

// VerifySigned is the promote job's trust root. It verifies the minisign
// signature of dir/cascade_checksums.txt with pub FIRST, requires the trusted
// comment to be exactly "cascade <finalTag without v> checksums", then
// requires every file in dir to equal its line in the signed checksums with
// no file missing and none extra (beyond the checksums file, its signature
// and the unsigned cask). The signed cascade_images.txt lines are returned
// in Report.Images. Nothing in a record or dispatch input is consulted.
func VerifySigned(dir string, pub minisign.PublicKey, finalTag string) (Report, error) {
	if !finalTagRe.MatchString(finalTag) {
		return Report{}, invalid("final tag %q is not v2.Y.Z", finalTag)
	}
	checksums, err := readSetFile(dir, checksumsName)
	if err != nil {
		return Report{}, err
	}
	signature, err := readSetFile(dir, signatureName)
	if err != nil {
		return Report{}, err
	}
	sig, err := verifySignature(checksums, signature, pub)
	if err != nil {
		return Report{}, err
	}
	if err = checkComment(sig, finalTag); err != nil {
		return Report{}, err
	}
	want, err := parseChecksums(checksums)
	if err != nil {
		return Report{}, err
	}
	if _, _, err = classify(want, strings.TrimPrefix(finalTag, "v")); err != nil {
		return Report{}, err
	}
	want[checksumsName], want[signatureName] = digest(checksums), digest(signature)
	got, err := SetFromDir(dir)
	if err != nil {
		return Report{}, err
	}
	if err = compareSet("signed set", want, got, true); err != nil {
		return Report{}, err
	}
	return signedImages(dir, want)
}

// signedImages re-reads cascade_images.txt, re-checks it against its signed
// line and parses it into the Report.
func signedImages(dir string, want map[string]string) (Report, error) {
	b, err := readSetFile(dir, imagesName)
	if err != nil {
		return Report{}, err
	}
	if digest(b) != want[imagesName] {
		return Report{}, refused("%s differs from its signed checksums line", imagesName)
	}
	images, err := parseImages(b)
	if err != nil {
		return Report{}, err
	}
	return Report{Files: slices.Sorted(maps.Keys(want)), Images: images}, nil
}

// RequireSignedImage returns the image name a digest is signed for in a
// VerifySigned report, refusing any digest the signed cascade_images.txt
// does not list. The promote image step copies and signs only digests that
// pass here.
func RequireSignedImage(rep Report, imageDigest string) (string, error) {
	if !imageLineRe.MatchString(imageDigest + "  x") {
		return "", invalid("image digest %q is not sha256:<64 hex>", imageDigest)
	}
	for _, name := range slices.Sorted(maps.Keys(rep.Images)) {
		if rep.Images[name] == imageDigest {
			return name, nil
		}
	}
	return "", refused("image digest %s is not in the signed %s", imageDigest, imagesName)
}

// CheckKey is the pages-keys gate: published (docs/keys/minisign.pub) must
// parse as a minisign public key, be byte-identical to embedded
// (internal/minisign/release.pub) and parse to the trusted key compiled
// into the binary. Anything else refuses before a Pages deploy.
func CheckKey(published, embedded []byte, trusted minisign.PublicKey) error {
	key, err := minisign.ParsePublicKey(published)
	if err != nil {
		return refused("the published key does not parse: %v", err)
	}
	if !bytes.Equal(published, embedded) {
		return refused("the published key is not byte-identical to the embedded release key")
	}
	same := key.Algorithm == trusted.Algorithm && key.KeyID == trusted.KeyID &&
		len(trusted.Key) == ed25519.PublicKeySize && bytes.Equal(key.Key, trusted.Key)
	if !same {
		return refused("the published key is not the release key compiled into this binary")
	}
	return nil
}
