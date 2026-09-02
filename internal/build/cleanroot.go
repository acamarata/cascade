// Package build (this file) implements the clean-root gate from
// P1-E01-W1-S01-T8 (12-QUALITY-CONSTITUTION.md Art.10.1): every path at
// repo root must be on the RELEASE-STATE allowlist — the complete set the
// article names for v2.0.0, not today's Wave-1 tree (09-REVIEW-
// RESOLUTIONS.md §Round 7; R-14 confirms the fixture asserts the
// release-state set). Files the allowlist admits but that do not exist yet
// (CHANGELOG.md, install.sh, Makefile — all true of this repo today) are
// simply ABSENT, never a violation: the gate fails on an EXTRA path, never
// on a permitted path's absence.
//
// The gate scans the TRACKED tree (`git ls-files`, one path segment deep at
// root), never a raw filesystem walk: a raw walk would flag the two
// gitignored build artifacts this repo's own working copy carries
// (/cascade, /cascade.exe — see .gitignore) as clean-root violations, which
// they are not — Art.10.1 says "generated/ignored artifacts... are
// invisible to the tracked tree the gate scans", stated for exactly this
// reason.
package build

import (
	"os/exec"
	"strings"
)

// CleanRootAllowedFiles is the RELEASE-STATE root FILE allowlist
// (Art.10.1). CHANGELOG.md, install.sh, and Makefile are included per
// 09-REVIEW-RESOLUTIONS.md §Round 7 even though none exists in the repo
// yet — their eventual presence must never turn this gate red, and their
// current absence must never turn it red either.
var CleanRootAllowedFiles = map[string]bool{
	"README.md":        true,
	"LICENSE":          true,
	"CHANGELOG.md":     true,
	".gitignore":       true,
	"install.sh":       true,
	"go.mod":           true,
	"go.sum":           true,
	".golangci.yml":    true,
	".goreleaser.yaml": true,
	"Makefile":         true,
}

// CleanRootAllowedDirs is the RELEASE-STATE root DIRECTORY allowlist
// (Art.10.1).
var CleanRootAllowedDirs = map[string]bool{
	".github":   true,
	"cmd":       true,
	"internal":  true,
	"pkg":       true,
	"providers": true,
	"plugins":   true,
	"apps":      true,
	"docs":      true,
	"testdata":  true,
}

// CleanRootViolation is one tracked root-level path outside the
// RELEASE-STATE allowlist.
type CleanRootViolation struct {
	Path   string
	IsFile bool
}

// GitLsFilesRoot runs `git ls-files` in root and returns the repo-relative
// tracked paths, forward-slash-separated. It is the gate's sole source of
// "what is at root": tracked files only, so a gitignored build artifact
// (per .gitignore) never appears and can never be flagged.
func GitLsFilesRoot(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(string(out), "\x00")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\x00"), nil
}

// CheckCleanRoot classifies every tracked path in trackedFiles (as returned
// by GitLsFilesRoot) by its FIRST path segment: a bare filename at root
// must be on CleanRootAllowedFiles; anything with a "/" must have its
// leading directory on CleanRootAllowedDirs. Every violation is reported
// exactly once per offending top-level segment (a whole disallowed
// directory reports once, not once per file inside it).
func CheckCleanRoot(trackedFiles []string) []CleanRootViolation {
	seenDirViolation := make(map[string]bool)
	var out []CleanRootViolation
	for _, f := range trackedFiles {
		if f == "" {
			continue
		}
		idx := strings.IndexByte(f, '/')
		if idx < 0 {
			if !CleanRootAllowedFiles[f] {
				out = append(out, CleanRootViolation{Path: f, IsFile: true})
			}
			continue
		}
		topDir := f[:idx]
		if CleanRootAllowedDirs[topDir] {
			continue
		}
		if seenDirViolation[topDir] {
			continue
		}
		seenDirViolation[topDir] = true
		out = append(out, CleanRootViolation{Path: topDir, IsFile: false})
	}
	return out
}
