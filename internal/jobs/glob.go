package jobs

// Purpose: R-21.168 lease-scope normalization -- Scope, the minimal
//
//	repo-relative directory-prefix cover a caller pattern expands to at
//	acquire time, and the prefix-containment intersection test lease.go
//	builds mutual exclusion on.
//
// Inputs: a caller-supplied doublestar pattern (NormalizeScope), or a
//
//	previously-stored canonical form (ParseScope).
//
// Outputs: a Scope, or a typed KindInvalidInput error naming the
//
//	offending segment for a pattern that does not reduce to a finite
//	prefix cover.
//
// Constraints: R-21.168 supersedes R-16.37's doublestar-intersection
//
//	wording -- the normalized form is a SET of directory prefixes, a
//	trailing `/**` the only wildcard ever kept in that normalized form,
//	and a literal (non-wildcard) pattern is always read as a FILE path,
//	reduced to its own (non-recursive) parent directory -- never assumed
//	to itself be the directory to lease. github.com/bmatcuk/doublestar/v4
//	both validates the caller's glob syntax and (via SplitPattern) finds
//	the literal-prefix/meta-character split point; this file adds the
//	"only a trailing `/**` survives reduction" restriction on top, since
//	doublestar's own grammar permits far more than a finite prefix cover
//	can represent.
//
// SPORT: jobs/lease-model + jobs/glob-intersection (ADD, P1-E29-W6-S59-T2).

import (
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/acamarata/cascade/pkg/cascade"
)

// prefix is one normalized directory prefix. dir is repo-relative,
// slash-separated, never absolute, never containing "..", and "" denotes
// the repo root (covers everything). recursive is cosmetic bookkeeping
// for String()'s stored form only -- Intersects and prefixContains never
// read it, since R-21.168's containment rule is uniform regardless of how
// a prefix was derived (see this file's doc comment).
type prefix struct {
	dir       string
	recursive bool
}

// Scope is the normalized R-21.168 lease scope: a minimal (no member is a
// descendant of another), sorted, deduped set of directory prefixes.
type Scope struct {
	prefixes []prefix
}

// ErrMalformedScope is the sentinel wrapped by every NormalizeScope/
// ParseScope rejection: a pattern with syntactically invalid glob
// grammar, an absolute path, a ".." segment, or a wildcard placement that
// does not reduce to a finite prefix cover.
var ErrMalformedScope = cascade.New(cascade.KindInvalidInput, "jobs: malformed lease scope glob")

// NormalizeScope expands pattern into its minimal prefix cover. pattern
// may name several sub-patterns separated by commas -- each is reduced
// independently and the results merged into one minimal set. A pattern
// (or sub-pattern) that fails doublestar's own syntax validation, is
// absolute, escapes the repo root via "..", or places a wildcard
// anywhere other than a lone trailing "/**" is rejected with a typed
// KindInvalidInput error naming the offending segment -- fail-closed,
// never a permissive fallback.
func NormalizeScope(pattern string) (Scope, error) {
	parts := strings.Split(pattern, ",")
	prefixes := make([]prefix, 0, len(parts))
	for _, part := range parts {
		p, err := reduceOne(strings.TrimSpace(part))
		if err != nil {
			return Scope{}, err
		}
		prefixes = append(prefixes, p)
	}
	if len(prefixes) == 0 {
		return Scope{}, cascade.Wrap(cascade.KindInvalidInput, ErrMalformedScope, "jobs: empty lease scope glob")
	}
	return Scope{prefixes: minimize(prefixes)}, nil
}

// reduceOne reduces a single sub-pattern (no commas) to one prefix.
func reduceOne(p string) (prefix, error) {
	if p == "" {
		return prefix{}, cascade.Wrap(cascade.KindInvalidInput, ErrMalformedScope, "jobs: empty lease scope glob segment")
	}
	if strings.HasPrefix(p, "/") {
		return prefix{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedScope,
			"jobs: lease scope glob %q must be repo-relative, not absolute", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return prefix{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedScope,
				"jobs: lease scope glob %q escapes the repo root via \"..\"", p)
		}
	}
	if !doublestar.ValidatePattern(p) {
		return prefix{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedScope,
			"jobs: lease scope glob %q is not valid glob syntax", p)
	}
	if !strings.ContainsAny(p, "*?[{") {
		// Literal pattern: R-21.168 reads it as a FILE path, so the
		// covered scope is its own (non-recursive) parent directory.
		dir := normalizeDir(path.Dir(path.Clean(p)))
		return prefix{dir: dir, recursive: false}, nil
	}
	base, rem := doublestar.SplitPattern(p)
	if rem != "**" {
		return prefix{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedScope,
			"jobs: lease scope glob %q does not reduce to a finite prefix cover at segment %q "+
				"(only a trailing \"/**\" is permitted)", p, rem)
	}
	return prefix{dir: normalizeDir(base), recursive: true}, nil
}

// normalizeDir maps path.Dir/SplitPattern's "." (their shared "no better
// answer" sentinel) to "", this package's repo-root prefix value.
func normalizeDir(d string) string {
	if d == "." {
		return ""
	}
	return d
}

// minimize sorts prefixes and drops any entry that is a descendant of
// (or a duplicate of) another entry already kept, so the returned set
// has no redundant member. Two entries for the same dir merge into one,
// recursive if either was.
func minimize(in []prefix) []prefix {
	sorted := append([]prefix(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].dir < sorted[j].dir })
	var out []prefix
	for _, p := range sorted {
		redundant := false
		for i := range out {
			if out[i].dir == p.dir {
				if p.recursive {
					out[i].recursive = true
				}
				redundant = true
				break
			}
			if dirContains(out[i].dir, p.dir) {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, p)
		}
	}
	return out
}

// dirContains reports whether a (a repo-relative directory prefix, ""
// meaning repo root) contains b: b equals a, or b is a strict
// component-wise descendant of a.
func dirContains(a, b string) bool {
	if a == "" {
		return true
	}
	if a == b {
		return true
	}
	return strings.HasPrefix(b, a+"/")
}

// Intersects reports whether s and other share at least one point in the
// repo tree: R-21.168's total, decidable rule -- one normalized prefix is
// a prefix of, or equal to, another, checked over every pair.
func (s Scope) Intersects(other Scope) bool {
	for _, a := range s.prefixes {
		for _, b := range other.prefixes {
			if dirContains(a.dir, b.dir) || dirContains(b.dir, a.dir) {
				return true
			}
		}
	}
	return false
}

// String returns the canonical stored form: each prefix as "dir" (bare,
// non-recursive) or "dir/**" (recursive; the repo root recursive prefix
// prints as "**"), sorted, comma-joined. This is the exact TEXT persisted
// in resource_lease.scope_glob -- ParseScope(s.String()) round-trips.
func (s Scope) String() string {
	parts := make([]string, len(s.prefixes))
	for i, p := range s.prefixes {
		switch {
		case p.recursive && p.dir == "":
			parts[i] = "**"
		case p.recursive:
			parts[i] = p.dir + "/**"
		case p.dir == "":
			parts[i] = "."
		default:
			parts[i] = p.dir
		}
	}
	return strings.Join(parts, ",")
}

// ParseScope decodes a previously-normalized stored form (Scope.String's
// output) back into a Scope. It does not re-run pattern reduction --
// every stored entry is already bare or "dir/**" by construction, so
// decoding is a straight parse, never a second validation pass.
func ParseScope(stored string) (Scope, error) {
	if stored == "" {
		return Scope{}, cascade.Wrap(cascade.KindInvalidInput, ErrMalformedScope, "jobs: empty stored lease scope")
	}
	parts := strings.Split(stored, ",")
	prefixes := make([]prefix, 0, len(parts))
	for _, part := range parts {
		switch {
		case part == "**":
			prefixes = append(prefixes, prefix{dir: "", recursive: true})
		case part == ".":
			prefixes = append(prefixes, prefix{dir: "", recursive: false})
		case strings.HasSuffix(part, "/**"):
			prefixes = append(prefixes, prefix{dir: strings.TrimSuffix(part, "/**"), recursive: true})
		default:
			prefixes = append(prefixes, prefix{dir: part, recursive: false})
		}
	}
	return Scope{prefixes: minimize(prefixes)}, nil
}
