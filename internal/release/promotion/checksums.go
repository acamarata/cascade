package promotion

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The fixed file names of a dist-<id> set (docs/developer/release-train.md).
const (
	checksumsName = "cascade_checksums.txt"
	signatureName = "cascade_checksums.txt.minisig"
	imagesName    = "cascade_images.txt"
	// caskName is in dist-<id> but is not a checksums line: it is unsigned
	// and never published as a release asset, so it is the one file the
	// staging side may carry beyond the signed set.
	caskName = "Casks/cascade.rb"
)

// platforms are the five release platforms of .goreleaser.yaml (darwin and
// linux on amd64 and arm64, windows on amd64), sorted, as "<os>_<arch>".
var platforms = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64"}

// imageNames are the lines rc-publish writes to cascade_images.txt.
var imageNames = []string{"cascade", "cascade-linux-amd64", "cascade-linux-arm64"}

var (
	checksumLineRe = regexp.MustCompile(`^([0-9a-f]{64})  (\S+)$`)
	imageLineRe    = regexp.MustCompile(`^(sha256:[0-9a-f]{64})  (\S+)$`)
	anyArchiveRe   = regexp.MustCompile(`^cascade_.*\.(tar\.gz|zip)$`)
)

// digest returns the lowercase hex sha256 of b.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// archiveFile is the archive name goreleaser gives platform p at version v.
func archiveFile(version, p string) string {
	ext := ".tar.gz"
	if strings.HasPrefix(p, "windows_") {
		ext = ".zip"
	}
	return "cascade_" + version + "_" + p + ext
}

// splitLines splits a newline-terminated text file into its lines, refusing
// an empty file, a missing final newline, CR line ends and blank lines.
func splitLines(file string, data []byte) ([]string, error) {
	text := string(data)
	if text == "" || !strings.HasSuffix(text, "\n") || strings.Contains(text, "\r") {
		return nil, invalid("%s is empty, lacks a final newline or has CR line ends", file)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			return nil, invalid("%s line %d is blank", file, i+1)
		}
	}
	return lines, nil
}

// checkLineName refuses a checksums line name that could escape the set
// directory or that names one of the set's own control files.
func checkLineName(name string) error {
	bad := name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) ||
		name == checksumsName || name == signatureName || strings.HasPrefix(name, "-")
	if bad {
		return invalid("checksums line names %q, which is not a plain release file name", name)
	}
	return nil
}

// parseChecksums parses cascade_checksums.txt into name -> sha256.
func parseChecksums(data []byte) (map[string]string, error) {
	lines, err := splitLines(checksumsName, data)
	if err != nil {
		return nil, err
	}
	sums := make(map[string]string, len(lines))
	for i, l := range lines {
		m := checksumLineRe.FindStringSubmatch(l)
		if m == nil {
			return nil, invalid("%s line %d is malformed", checksumsName, i+1)
		}
		if err := checkLineName(m[2]); err != nil {
			return nil, err
		}
		if _, dup := sums[m[2]]; dup {
			return nil, invalid("%s names %s twice", checksumsName, m[2])
		}
		sums[m[2]] = m[1]
	}
	if _, ok := sums[imagesName]; !ok {
		return nil, invalid("%s has no %s line, so the image digests are unsigned", checksumsName, imagesName)
	}
	return sums, nil
}

// classify splits signed checksums into archives (exactly the five
// platforms at version) and every other line as an asset.
func classify(sums map[string]string, version string) (archives, assets map[string]string, err error) {
	archives, assets = map[string]string{}, map[string]string{}
	byName := map[string]string{}
	for _, p := range platforms {
		byName[archiveFile(version, p)] = p
	}
	for name, d := range sums {
		if p, ok := byName[name]; ok {
			archives[p] = d
			continue
		}
		if anyArchiveRe.MatchString(name) {
			return nil, nil, invalid("%s is an archive of no release platform at version %s", name, version)
		}
		assets[name] = d
	}
	for _, p := range platforms {
		if _, ok := archives[p]; !ok {
			return nil, nil, invalid("checksums lack the %s archive %s", p, archiveFile(version, p))
		}
	}
	return archives, assets, nil
}

// parseImages parses cascade_images.txt into image name -> digest, exactly
// the three names rc-publish writes.
func parseImages(data []byte) (map[string]string, error) {
	lines, err := splitLines(imagesName, data)
	if err != nil {
		return nil, err
	}
	images := make(map[string]string, len(lines))
	for i, l := range lines {
		m := imageLineRe.FindStringSubmatch(l)
		if m == nil {
			return nil, invalid("%s line %d is malformed", imagesName, i+1)
		}
		if _, dup := images[m[2]]; dup {
			return nil, invalid("%s names %s twice", imagesName, m[2])
		}
		images[m[2]] = m[1]
	}
	if err := checkImages(images); err != nil {
		return nil, err
	}
	return images, nil
}

// checkImages requires exactly the three image names, each with a sha256
// digest.
func checkImages(images map[string]string) error {
	if len(images) != len(imageNames) {
		return invalid("images has %d entries, want exactly %v", len(images), imageNames)
	}
	for _, n := range imageNames {
		if !imageLineRe.MatchString(images[n] + "  " + n) {
			return invalid("images[%s] is missing or not a sha256:<hex> digest", n)
		}
	}
	return nil
}

// refused builds a KindIntegrity refusal naming the file or check.
func refused(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "promotion: refused: "+format, args...)
}
