// Purpose: the per-profile runtime home, $CASCADE_HOME/profiles/<id>/. Each
//
//	account's runtime keeps its credentials and config in its own 0700
//	directory so two accounts can never read or overwrite each other's.
//
// Inputs: the CASCADE_HOME root (runtime.PathProvider.Root()) and a profile
//
//	id.
//
// Outputs: the home path, or ErrTopologyInvariant (bad id) /
//
//	ErrProfileHomePermission (cannot make or verify the directory).
//
// Constraints: the id is a path segment, never a path (traversal refused);
//
//	a symlink or file at the home path (or at profiles/) is refused; on
//	unix both directories must be owned by the effective user and are
//	opened O_NOFOLLOW, chmodded and stat-verified through the descriptor; windows declares a tier-2 refusal
//	(profile_home_windows.go): it accepts an existing directory only.
//
// SPORT: fleet/topology/profile_home/ADD (P1-TOP-01).

package topology

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// profilesDirName is the directory under the root that holds every home.
const profilesDirName = "profiles"

// ErrProfileHomePermission reports that a profile home could not be created,
// tightened to 0700 or verified (or, on windows, does not already exist).
var ErrProfileHomePermission = cascade.New(cascade.KindPermissionDenied, "topology: profile home permission")

// profileIDPattern is the closed shape of a profile id used as a path segment.
var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Seams for the filesystem steps tests must fail (the unix leg adds
// effectiveUID in profile_home_unix.go). Unexported and never set
// by production code beyond the per-platform init; tests replace them,
// nothing at runtime can. ensureHomeDir defaults to a refusal so a platform
// without a leg (profile_home_unix.go, profile_home_windows.go) fails closed.
var (
	chmodHome     = func(f *os.File, m fs.FileMode) error { return f.Chmod(m) }
	ensureHomeDir = refuseHome
)

// refuseHome is the fail-closed default leg for a platform with no
// implementation.
func refuseHome(path string) error {
	return homePermErr(path, "profile homes are not supported on this platform")
}

// HomePath returns <root>/profiles/<id>. The id must match
// ^[a-z0-9][a-z0-9._-]{0,63}$, contain no "..", not end in "." (windows
// folds "a." onto "a", which would merge two accounts' homes) and not start
// with a windows reserved device name (con, prn, aux, nul, com0-9, lpt0-9,
// with or without an extension), else ErrTopologyInvariant naming the id.
// root must be absolute. It performs no filesystem access.
func HomePath(root string, id RuntimeProfileID) (string, error) {
	s := string(id)
	if !profileIDPattern.MatchString(s) || containsDotDot(s) || strings.HasSuffix(s, ".") || isReservedDeviceName(s) {
		return "", newInvariantErr("runtime_profile", s, "id is not a safe path segment")
	}
	if root == "" {
		return "", newInvariantErr("runtime_profile", s, "empty runtime root")
	}
	if !filepath.IsAbs(root) {
		return "", newInvariantErr("runtime_profile", s, "runtime root must be an absolute path")
	}
	return filepath.Join(root, profilesDirName, s), nil
}

// isReservedDeviceName reports whether the stem of s (the text before its
// first '.') is a windows reserved device name. Windows opens "con.txt" as
// the console, so such an id is unusable as a directory there.
func isReservedDeviceName(s string) bool {
	stem, _, _ := strings.Cut(s, ".")
	switch stem {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt")) {
		return stem[3] >= '0' && stem[3] <= '9'
	}
	return false
}

// containsDotDot reports whether s holds two consecutive dots.
func containsDotDot(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] == '.' && s[i-1] == '.' {
			return true
		}
	}
	return false
}

// EnsureHome creates (or verifies) the profile home and returns its path.
// Unix: profiles/ and the home are opened without following links, must be
// directories owned by the effective user, and are chmodded to 0700 through
// the open descriptor; the mode is verified afterwards and a second call is
// a no-op returning the same path. A non-directory, a symlink, a foreign
// owner, or a stat/chmod failure returns ErrProfileHomePermission and no path.
func EnsureHome(root string, id RuntimeProfileID) (string, error) {
	path, err := HomePath(root, id)
	if err != nil {
		return "", err
	}
	if err := ensureHomeDir(path); err != nil {
		return "", err
	}
	return path, nil
}

// homePermErr wraps ErrProfileHomePermission with the path and reason.
func homePermErr(path, reason string) error {
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrProfileHomePermission, "profile home %q: %s", path, reason)
}
