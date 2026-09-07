package census

import "strings"

// Purpose: the R-21.271 account-attribution match — a pure function with
//
//	no external calls and no file I/O beyond the platform read the caller
//	already performed.
//
// Inputs: argv (already read by the platform backend) and accountDirs, a
//
//	caller-supplied map of known Cascade account directory path -> account
//	alias.
//
// Outputs: the matched alias, or "" — never an error. "" covers both an
//
//	unresolvable process (no argv token contains any known directory) and
//	an AMBIGUOUS one (argv tokens match more than one distinct alias):
//	per this ticket's non-negotiables, an ambiguous attribution is
//	reported as unknown explicitly, never guessed by picking one match
//	arbitrarily.
//
// Constraints: argv-only. This function never reads process environment,
//
//	on any platform (R-21.271's extension of R-21.152) — a platform
//	backend's environment read, if it had one, would already have been
//	discarded before argv reaches here. No global state is read or
//	mutated; the same (argv, accountDirs) pair always yields the same
//	result.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

// attributeAccount scans argv for a token containing one of accountDirs'
// known directory paths. Map iteration order is not relied on: every
// matching directory's alias is collected first, and the result is only
// returned when exactly one distinct alias matched, so the outcome is
// deterministic regardless of Go's randomized map iteration.
func attributeAccount(argv []string, accountDirs map[string]string) string {
	matched := make(map[string]bool)
	for _, tok := range argv {
		for dir, alias := range accountDirs {
			if dir == "" || alias == "" {
				continue
			}
			if strings.Contains(tok, dir) {
				matched[alias] = true
			}
		}
	}
	if len(matched) != 1 {
		return ""
	}
	for alias := range matched {
		return alias
	}
	return ""
}
