// Purpose: map a release artifact tag to its Go module twin (§D-33 dual
//
//	tags). The binary ships as v2.Y.Z[-pre] while the Go module stays on the
//	v0 line, so every release cut creates both tags from one mapping.
//
// Inputs:  an artifact tag such as v2.0.0 or v2.0.0-rc.1.
// Outputs: the module tag (v0.Y.Z[-pre]) or a KindInvalidInput error.
// Constraints: pure, no I/O. Only major 2 maps; any other major or a
//
//	malformed tag is refused, never guessed. See
//	docs/developer/release-train.md (dual tags).
//
// SPORT: internal/buildinfo — release tag mapping (contract:signed-release-artifacts).

package buildinfo

import (
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// semverPrereleaseID is one semver prerelease identifier: a number without
// leading zeros, or an alphanumeric that is not all digits.
const semverPrereleaseID = `(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`

// artifactTagPattern accepts v2.<minor>.<patch> with an optional semver
// prerelease suffix and no build metadata. Numeric parts carry no leading
// zeros, so every accepted tag maps to a valid Go module version.
var artifactTagPattern = regexp.MustCompile(`^v2\.((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-` +
	semverPrereleaseID + `(?:\.` + semverPrereleaseID + `)*)?)$`)

// ModuleTagFor returns the Go module tag for artifactTag: v2.Y.Z[-pre]
// becomes v0.Y.Z[-pre]. Any other major, a missing "v" prefix or a
// malformed version returns a cascade.KindInvalidInput error.
func ModuleTagFor(artifactTag string) (string, error) {
	m := artifactTagPattern.FindStringSubmatch(artifactTag)
	if m == nil {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"buildinfo: artifact tag %q is not v2.Y.Z[-prerelease]", artifactTag)
	}
	return "v0." + m[1], nil
}
