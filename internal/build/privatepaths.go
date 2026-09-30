// Package build (this file) implements the private-tracked-path gate from
// P1-SHIP-14 (AUD-041): every path under .claude/, .opencode/ or .cascade/
// (private-workspace trees, including .claude/hygiene/identifier-patterns.txt,
// sweep.go's own pattern-file source) must never be a TRACKED file in this
// public repo.
//
// # Why a gate over `git ls-files`, not a stronger .gitignore
//
// .gitignore (the "AI workspace (never tracked)" block) already excludes
// these trees from ordinary `git add .` — but `git add -f` bypasses any
// ignore rule by design, and no git hook can refuse an `add`; the only
// enforcement point that actually sees the outcome is what ends up tracked.
// This gate reads exactly that: `git ls-files`, the same source
// ListTrackedFiles (sweep.go) and GitLsFilesRoot (cleanroot.go) already use,
// so a force-added private path is caught at build/CI/ship time even though
// nothing stopped the `add` itself.
package build

import "strings"

// PrivateTrackedPrefixes are the private-workspace path prefixes that must
// never appear as a tracked file, matched against a repo-relative,
// forward-slash path exactly as `git ls-files` reports it. This is the
// single source of truth for which directories are private, derived below
// into privateTrackedSegments, the actual gate, which matches path
// SEGMENTS rather than merely a string prefix, so a nested private dir
// (e.g. apps/ui/.claude/x, force-added with `git add -f`) is caught too.
var PrivateTrackedPrefixes = []string{".claude/", ".opencode/", ".cascade/"}

// privateTrackedSegments are the exact path-segment names (never a
// prefix/substring), derived from PrivateTrackedPrefixes and lower-cased,
// that make any tracked path private, no matter how deep it sits. `git add
// -f` bypasses .gitignore entirely, so a private dir nested under an
// otherwise-allowed root (apps/ui/.claude/x, a/b/.opencode/y, c/.cascade/z)
// is exactly as much a bypass as a root-level one. Segments are compared
// case-insensitively: on a case-insensitive filesystem git (core.ignorecase)
// applies `.claude/` in .gitignore to `.Claude` too, so `.Claude`,
// `.OpenCode` and `.CASCADE` are the same private dir. ".claudex" or
// "x.claude" are different segments and never match.
var privateTrackedSegments = func() map[string]bool {
	m := make(map[string]bool, len(PrivateTrackedPrefixes))
	for _, p := range PrivateTrackedPrefixes {
		m[strings.ToLower(strings.TrimSuffix(p, "/"))] = true
	}
	return m
}()

// privateTrackedFixtureAllow is the exact, root-anchored list of
// repo-relative prefixes whose private-dir segments are tracked on purpose
// as seeded test fixture data. It is deliberately NOT SweepSkipsPath's
// "any segment is testdata" rule: that would exempt .claude/testdata/x,
// apps/ui/.claude/testdata/x and testdata/.claude/x alike. Today exactly one
// tracked path needs it (internal/migration/testdata/fixture_project/
// .claude/CLAUDE.md); a new entry is a reviewed edit here.
var privateTrackedFixtureAllow = []string{"internal/migration/testdata/fixture_project/"}

// CheckNoPrivateTracked scans tracked (as returned by ListTrackedFiles or
// GitLsFilesRoot) for any path that has a private-workspace directory
// (privateTrackedSegments, case-insensitive) as one of its path segments,
// at any depth, and returns every match, in input order. Only paths under
// an exact privateTrackedFixtureAllow prefix are exempt; a `testdata`
// segment anywhere else exempts nothing. An empty result means the tree is
// clean; it is never treated as "nothing to check" — a caller with a
// suspiciously empty tracked list should already have failed upstream
// (e.g. TestCleanRoot_RealTreeGreen's zero-files check), not here.
func CheckNoPrivateTracked(tracked []string) []string {
	var out []string
	for _, f := range tracked {
		if f == "" || privateTrackedFixtureAllowed(f) {
			continue
		}
		for _, seg := range strings.Split(f, "/") {
			if privateTrackedSegments[strings.ToLower(seg)] {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// privateTrackedFixtureAllowed reports whether f sits under one of the
// exact, root-anchored privateTrackedFixtureAllow prefixes.
func privateTrackedFixtureAllowed(f string) bool {
	for _, p := range privateTrackedFixtureAllow {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}
