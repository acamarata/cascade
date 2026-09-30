package runtime

// Purpose: vetDiff — every check ApplyDiff runs on a ConfigDiff before it
// reads config.toml (P1-E25-W5-S103-T1): the owner is one bare key, no
// path repeats, no entry reaches a guarded family, another plugin's
// table, a bare [plugins] key or the owner's own managed record, every
// literal passes the shared canonicalLiteral validator, and no value is
// secret-shaped or a URL carrying userinfo.
// Inputs: a ConfigDiff.
// Outputs: the entries as vettedEntry (decoded value + canonical text), or
// the first refusal: KindPolicyDenied for an authority violation,
// KindInvalidInput for a malformed owner; KindPolicyDenied for a duplicate,
// *LiteralError, *SecretLiteralError, or the path resolver's own error.
// Constraints: authority checks run on syntax alone (SplitDottedPath),
// before the known-key registry, so a guarded or foreign path is refused
// as a policy decision even when the registry would also reject it.
// SPORT: internal/runtime config_diff.go family (ADD) — P1-E25-W5-S103-T1.

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// managedKey is the key under [plugins.<owner>] that only ApplyDiff writes.
const managedKey = "managed"

// vettedEntry is one DiffEntry after vetting: Canonical is the only text
// ApplyDiff ever writes for it.
type vettedEntry struct {
	Path      string
	Value     interface{}
	Canonical string
}

// guardedDiffFamilies is the closed set of top-level segments ApplyDiff
// refuses: baselineGuardedSections (the six CompareSecurity families,
// read from there so the lists cannot disagree) plus "agents" (the
// [agents.egress] allowlists, which CompareSecurity does not cover).
func guardedDiffFamilies() []string {
	return append(append([]string{}, baselineGuardedSections...), "agents")
}

// isGuardedFamily reports whether segment names a guarded family.
func isGuardedFamily(segment string) bool {
	for _, g := range guardedDiffFamilies() {
		if segment == g {
			return true
		}
	}
	return false
}

// vetDiff runs every per-diff and per-entry check.
func vetDiff(diff ConfigDiff) ([]vettedEntry, error) {
	if !bareKeyPattern.MatchString(diff.Owner) {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"runtime: ApplyDiff: owner %q must be one bare TOML key", diff.Owner)
	}
	seen := map[string]bool{}
	out := make([]vettedEntry, 0, len(diff.Entries))
	for _, e := range diff.Entries {
		if seen[e.Path] {
			return nil, cascade.Newf(cascade.KindPolicyDenied,
				"runtime: ApplyDiff: path %s appears twice in one diff", e.Path)
		}
		seen[e.Path] = true
		v, err := vetEntry(diff.Owner, e)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// vetEntry checks one entry's authority, key, literal and value.
func vetEntry(owner string, e DiffEntry) (vettedEntry, error) {
	segments, err := SplitDottedPath(e.Path)
	if err != nil {
		return vettedEntry{}, err
	}
	if err := vetPathAuthority(owner, e.Path, segments); err != nil {
		return vettedEntry{}, err
	}
	if _, err := ResolveDottedPath(e.Path); err != nil {
		return vettedEntry{}, err
	}
	value, canonical, err := vetLiteral(e.Path, e.Literal)
	var secret *SecretLiteralError
	if errors.As(err, &secret) {
		return vettedEntry{}, cascade.Wrap(cascade.KindPolicyDenied, err, "runtime: ApplyDiff: secret literal refused")
	}
	if err != nil {
		return vettedEntry{}, err
	}
	return vettedEntry{Path: e.Path, Value: value, Canonical: canonical}, nil
}

// vetLiteral is the ONE value validator Set, ApplyDiff and
// ScreenConfigLiteral share: canonicalLiteral (exactly one TOML value)
// then screenDiffValue (config_diff_screen.go: no secret-shaped or
// userinfo-bearing string or inline-table key).
func vetLiteral(path, literal string) (interface{}, string, error) {
	value, canonical, err := canonicalLiteral(literal)
	if err != nil {
		return nil, "", err
	}
	if err := screenDiffValue(path, value); err != nil {
		return nil, "", err
	}
	return value, canonical, nil
}

// ScreenConfigLiteral reports whether literal would pass the value checks
// Set and ApplyDiff run at path (vetLiteral). A plugin that proposes a
// diff without writing it (cascade-nself's handshake, bound through
// internal/plugins) calls this so it never proposes a value the writer
// would refuse. It returns a *LiteralError or *SecretLiteralError; it
// never checks path authority, which stays ApplyDiff's job.
func ScreenConfigLiteral(path, literal string) error {
	_, _, err := vetLiteral(path, literal)
	return err
}

// vetPathAuthority refuses a guarded family, any [plugins] path outside
// [plugins.<owner>] (a bare [plugins] key such as enable_remote_runtime
// included), and any path at or under [plugins.<owner>].managed, which
// only ApplyDiff itself writes: accepting it would let a diff forge the
// ownership record that decides whether a user edit is overwritten.
func vetPathAuthority(owner, path string, segments []string) error {
	if isGuardedFamily(segments[0]) {
		return cascade.Newf(cascade.KindPolicyDenied,
			"runtime: ApplyDiff: %q is a guarded family; refusing to write %s", segments[0], path)
	}
	if segments[0] != "plugins" {
		return nil
	}
	if len(segments) < 3 || segments[1] != owner {
		return cascade.Newf(cascade.KindPolicyDenied,
			"runtime: ApplyDiff: %s is outside the owner's own table [plugins.%s]", path, owner)
	}
	if segments[2] == managedKey {
		return cascade.Newf(cascade.KindPolicyDenied,
			"runtime: ApplyDiff: %s is the ownership record; only ApplyDiff writes it", path)
	}
	return nil
}
