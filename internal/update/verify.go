package update

// Purpose: fetch a release and prove it before anything can be installed:
//   the signed checksums, this platform's one archive line, the version the
//   signature binds, the archive digest, and a safe extraction.
// Inputs: a Fetcher, the Source, the tag, the Platform, the trust root and
//   an empty work directory.
// Outputs: a VerifiedArchive, or a refusal that leaves no executable in
//   workDir.
// Constraints: the order is fixed and every step fails closed:
//   checksums + .minisig -> minisign.Verify (the one verifier) -> exactly one
//   archive entry -> trusted-comment version == archive-name version (and ==
//   the tag in base mode) -> archive sha256 -> safe extract. Nothing is read
//   from the archive before its digest matches the signed line.
// SPORT: internal/update FetchVerified/ADDED.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/pkg/cascade"
)

// checksumsName is the version-free checksums file goreleaser publishes.
const checksumsName = "cascade_checksums.txt"

// maxSignatureBytes bounds the .minisig file.
const maxSignatureBytes = 4 << 10

// maxArchiveBytes bounds an archive download.
const maxArchiveBytes = 512 << 20

// Platform is an OS/arch pair in goreleaser's naming.
type Platform struct{ OS, Arch string }

// VerifiedArchive is a verified, extracted release archive. Dir holds the
// extracted tree; Executables are the fixed-set names found at its root,
// helper and plugin first, cascade last.
type VerifiedArchive struct {
	ReleaseTag, ArchiveName, ArchiveSHA256, SignedVersion, Dir string
	Executables                                                []string
}

// signedRelease is the verified checksums half: no archive bytes yet.
type signedRelease struct {
	Tag, Version string
	Entry        checksumEntry
}

// fetchSigned fetches and verifies the checksums file and selects the
// archive entry. Check uses it alone; FetchVerified continues from it.
func fetchSigned(ctx context.Context, f Fetcher, s Source, tag string, p Platform, pub minisign.PublicKey) (signedRelease, error) {
	sums, err := fetchCapped(ctx, f, s.fileURL(tag, checksumsName), maxChecksumsBytes, checksumsName)
	if err != nil {
		return signedRelease{}, err
	}
	rawSig, err := fetchCapped(ctx, f, s.fileURL(tag, checksumsName+".minisig"), maxSignatureBytes, checksumsName+".minisig")
	if err != nil {
		return signedRelease{}, err
	}
	sig, err := minisign.ParseSignature(rawSig)
	if err != nil {
		return signedRelease{}, cascade.Wrap(cascade.KindIntegrity, err, "update: the checksums signature does not verify")
	}
	if err := minisign.Verify(pub, sums, sig); err != nil {
		return signedRelease{}, cascade.Wrap(cascade.KindIntegrity, err, "update: the checksums signature does not verify")
	}
	version, err := signedVersion(sig.TrustedComment)
	if err != nil {
		return signedRelease{}, err
	}
	entries, err := parseChecksums(sums)
	if err != nil {
		return signedRelease{}, err
	}
	entry, archiveVersion, err := selectArchive(entries, p)
	if err != nil {
		return signedRelease{}, err
	}
	if archiveVersion != version {
		return signedRelease{}, integrity("version mismatch: the signature names %s but the archive %s names %s", version, entry.Name, archiveVersion)
	}
	if s.Dist == "" && strings.TrimPrefix(tag, "v") != version {
		return signedRelease{}, integrity("version mismatch: the signature names %s but the release tag is %s", version, tag)
	}
	return signedRelease{Tag: tag, Version: version, Entry: entry}, nil
}

// FetchVerified runs the whole verification and extracts into workDir.
func FetchVerified(ctx context.Context, f Fetcher, s Source, tag string, p Platform, pub minisign.PublicKey, workDir string) (VerifiedArchive, error) {
	rel, err := fetchSigned(ctx, f, s, tag, p, pub)
	if err != nil {
		return VerifiedArchive{}, err
	}
	archivePath := filepath.Join(workDir, "archive")
	defer func() { _ = os.Remove(archivePath) }()
	if err := downloadVerified(ctx, f, s.fileURL(tag, rel.Entry.Name), archivePath, rel.Entry.SHA256); err != nil {
		return VerifiedArchive{}, err
	}
	dir := filepath.Join(workDir, "x")
	exes, err := extractArchive(archivePath, rel.Entry.Name, dir, p.OS)
	if err != nil {
		_ = os.RemoveAll(dir)
		return VerifiedArchive{}, err
	}
	return VerifiedArchive{ReleaseTag: tag, ArchiveName: rel.Entry.Name, ArchiveSHA256: rel.Entry.SHA256,
		SignedVersion: rel.Version, Dir: dir, Executables: exes}, nil
}

// fetchCapped fetches url fully, refusing more than limit bytes.
func fetchCapped(ctx context.Context, f Fetcher, url string, limit int64, what string) ([]byte, error) {
	rc, err := f.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	return readCapped(rc, limit, what)
}

// downloadVerified streams url into path and refuses (removing path) unless
// its sha256 equals want.
func downloadVerified(ctx context.Context, f Fetcher, url, path, want string) error {
	rc, err := f.Get(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "update: create %s", path)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, maxArchiveBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = cascade.Wrapf(cascade.KindUnavailable, err, "update: download %s", url)
	case n > maxArchiveBytes:
		err = integrity("the archive is larger than %d bytes", maxArchiveBytes)
	case hex.EncodeToString(h.Sum(nil)) != want:
		err = integrity("sha256 mismatch for the downloaded archive")
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
