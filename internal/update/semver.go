package update

// Purpose: semver 2.0.0 precedence for the downgrade rule, so
//   2.0.0-rc.1 < 2.0.0 < 2.0.1.
// Inputs: version strings with an optional leading "v"; build metadata
//   (+...) is validated and ignored for precedence, as semver requires.
// Outputs: a comparison, or KindIntegrity for a version that does not parse.
// Constraints: full SemVer 2.0.0 syntax: three numeric core identifiers,
//   dot-separated non-empty [0-9A-Za-z-] prerelease and build identifiers,
//   no leading zero in a numeric identifier. A numeric identifier is one of
//   digits only, of any length: numbers compare by length, then lexically
//   (no integer parse, so no overflow). "dev" (the unstamped
//   buildinfo.Version) is 0.0.0-dev.
// SPORT: internal/update compareVersions/ADDED.

import "strings"

// devVersion is buildinfo.Version for an unstamped build.
const devVersion = "dev"

type semver struct {
	core [3]string
	pre  []string
}

// parseSemver parses MAJOR.MINOR.PATCH[-PRE][+BUILD].
func parseSemver(v string) (semver, error) {
	if v == devVersion {
		v = "0.0.0-dev"
	}
	bad := integrity("version %q is not semver", v)
	s, build, hasBuild := strings.Cut(strings.TrimPrefix(v, "v"), "+")
	if hasBuild && !validIdents(build, false) {
		return semver{}, bad
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, bad
	}
	var out semver
	for i, p := range parts {
		if !isNumeric(p) || !validNumeric(p) {
			return semver{}, bad
		}
		out.core[i] = p
	}
	if hasPre {
		if !validIdents(pre, true) {
			return semver{}, bad
		}
		out.pre = strings.Split(pre, ".")
	}
	return out, nil
}

// validIdents checks dot-separated identifiers; pre also refuses a leading
// zero in a numeric one (build metadata allows it).
func validIdents(s string, pre bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" || strings.Trim(id, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-") != "" {
			return false
		}
		if pre && isNumeric(id) && !validNumeric(id) {
			return false
		}
	}
	return true
}

// isNumeric reports an identifier of ASCII digits only.
func isNumeric(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// validNumeric refuses a leading zero ("0" itself is fine).
func validNumeric(id string) bool { return len(id) == 1 || id[0] != '0' }

// compareNumeric orders two valid numeric identifiers of any length.
func compareNumeric(a, b string) int {
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return strings.Compare(a, b)
}

// compareVersions returns -1, 0 or 1 by semver precedence.
func compareVersions(a, b string) (int, error) {
	x, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	y, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range x.core {
		if c := compareNumeric(x.core[i], y.core[i]); c != 0 {
			return c, nil
		}
	}
	return comparePre(x.pre, y.pre), nil
}

// comparePre orders prerelease identifiers; no prerelease ranks highest.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// compareIdent: numeric identifiers (digits only) compare numerically and
// rank below alphanumeric ones, which compare in ASCII order.
func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		return compareNumeric(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
