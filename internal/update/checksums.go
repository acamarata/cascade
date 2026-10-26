package update

// Purpose: parse the signed checksums file, select this platform's single
//   archive line, and read the version the signature binds.
// Inputs: the checksums bytes (already signature-verified by the caller),
//   the trusted comment, a Platform.
// Outputs: entries, the one selected entry and its version, or a
//   KindIntegrity refusal.
// Constraints: every line is "<64 lowercase hex><space(s)><bare name>"; any
//   other line, or a name listed twice, refuses the whole file. Exactly one
//   name may match ^cascade_(<version>)_<os>_<arch>\.(tar\.gz|zip)$, so
//   "<archive>.sbom.json" lines never match. The trusted comment must be
//   exactly "cascade <version> checksums" and equal the archive version.
// SPORT: internal/update parseChecksums/ADDED.

import (
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxChecksumsBytes bounds the checksums file; a real one is a few KiB.
const maxChecksumsBytes = 1 << 20

// versionChars is the version alphabet install.sh and the archive regex use.
const versionChars = `[0-9A-Za-z.+-]+`

var trustedCommentRE = regexp.MustCompile(`^cascade (` + versionChars + `) checksums$`)

// checksumEntry is one "<sha256>  <name>" line.
type checksumEntry struct {
	SHA256, Name string
}

func integrity(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "update: "+format, args...)
}

// parseChecksums parses a sha256sum-format file, failing closed on any
// malformed line.
func parseChecksums(data []byte) ([]checksumEntry, error) {
	if len(data) > maxChecksumsBytes {
		return nil, integrity("checksums file is larger than %d bytes", maxChecksumsBytes)
	}
	var out []checksumEntry
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !isHexDigest(fields[0]) || strings.ContainsAny(fields[1], `/\`) || strings.HasPrefix(fields[1], "*") {
			return nil, integrity("checksums file line %d is malformed", i+1)
		}
		if seen[fields[1]] {
			return nil, integrity("checksums file lists %s twice", fields[1])
		}
		seen[fields[1]] = true
		out = append(out, checksumEntry{SHA256: fields[0], Name: fields[1]})
	}
	if len(out) == 0 {
		return nil, integrity("checksums file is empty")
	}
	return out, nil
}

// archivePattern matches this platform's archive name and captures its version.
func archivePattern(p Platform) *regexp.Regexp {
	return regexp.MustCompile(`^cascade_(` + versionChars + `)_` + regexp.QuoteMeta(p.OS) + `_` +
		regexp.QuoteMeta(p.Arch) + `\.(tar\.gz|zip)$`)
}

// selectArchive returns the single entry for p and the version its name carries.
func selectArchive(entries []checksumEntry, p Platform) (checksumEntry, string, error) {
	re := archivePattern(p)
	var hit checksumEntry
	var version string
	n := 0
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name); m != nil {
			hit, version = e, m[1]
			n++
		}
	}
	if n != 1 {
		return checksumEntry{}, "", integrity("found %d archive entries for %s/%s in the signed checksums, want exactly 1", n, p.OS, p.Arch)
	}
	return hit, version, nil
}

// signedVersion reads the version from the trusted comment.
func signedVersion(trustedComment string) (string, error) {
	m := trustedCommentRE.FindStringSubmatch(trustedComment)
	if m == nil {
		return "", integrity("version mismatch: the trusted comment is not of the form `cascade <version> checksums`")
	}
	return m[1], nil
}
