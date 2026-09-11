// Package sport is the queryable SPORT entity registry the owner asked
// for, distinct from internal/inventory's SPORTLines field (a raw line
// count, not deduplicated, not structured, not queryable — see that
// package's doc comment). This package answers "what entities exist and
// what is each one's status", not just "how many marker lines are there".
//
// Schema derivation: the schema below was derived by scanning every
// "// SPORT:" line this tree carries (1178 as of this package's first
// generation) and grouping the forms actually found, not by assuming one.
// The dominant shape is:
//
//	// SPORT: <name>/ADDED (<ticket>).
//	// SPORT: <name> (ADD, <detail>).
//	// SPORT: <name> [ADD] (<ticket>).
//
// with <name> a dotted (internal.audit.Log) or slashed (cmd/cascade/node)
// path, sometimes with a trailing bare symbol joined by a space
// (internal/secrets VaultEnvImport). One marker line may declare several
// entities separated by top-level commas (commas inside parens/brackets
// are the entity's own detail text, not a separator — the doc comment on
// internal/inventory's SPORTLines field gives the three-entity example
// this rule exists for).
//
// Status verbs found: ADD, ADDED, CHANGE, CHANGED, CHG (an abbreviation
// for CHANGED — found once, cmd/cascade/node.go). REMOVE/REMOVED/
// DEPRECATE/DEPRECATED never appear in the live tree but are recognized
// aliases (this repo has not yet removed or deprecated a tracked entity
// through this marker; the parser accepts the verb rather than assuming
// it can never occur).
//
// Non-conforming forms found (50 of 1178 marker lines, ~4.2%): a
// description with no recognized status verb at all, e.g.
// "cmd/cascade — cobra-root, global-flags, version, completions." These
// are not silently dropped — Parse records them as an Entity with
// Status "" (StatusUnspecified) rather than discarding the line, because
// a silently-skipped line is an entity that vanishes from the registry
// (the owner's own framing for why SPORTLines was not enough). They are
// reported separately (Registry.Unspecified) so a caller can tell
// "parsed, but the author never stated a status" apart from "parsed
// cleanly".
//
// Genuinely malformed lines (a marker with no extractable name at all —
// blank, or pure punctuation after "SPORT:") are a HARD failure from
// Parse, never skipped: see parse.go's doc comment for why this is a hard
// failure while an unspecified status is only reported.
//
// Deduplication: BuildRegistry groups every parsed entity by Name across
// every declaring file into one Entity carrying every Site (file, line,
// status, ticket, raw text) that declared it. An entity re-declared with
// a DIFFERENT status across sites (e.g. ADD once, CHANGED later) is
// normal history, not a contradiction — see registry.go's doc comment for
// where the line is actually drawn.
package sport
