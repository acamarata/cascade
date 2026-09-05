// Package policy (denylist.go): Purpose: the vocabulary of the
//
//	configurable deny-list — the rule row, the engine interface layer 1
//	and the approval queue consume, the two named refusals, the pattern
//	grammar and the matcher. The B-layer-backed store is in
//	denylist_store.go and the normalizer that produces the matcher's input
//	is in denylist_normalize.go (sibling files per R-14.117 / Art.10.3).
//
// Inputs: an operator-supplied pattern and the action class it is scoped
//
//	to, and the command text an evaluation is asking about.
//
// Outputs: whether the action is listed, the rows themselves, or a typed
//
//	refusal carrying CodeDenyListPatternInvalid or CodeDenyListStoreError.
//
// Constraints: EVERY failure direction DENIES. A pattern that will not
//
//	parse, a row that will not decode, a store that cannot be read and a
//	command the normalizer cannot resolve all surface as an error, and
//	layer 1 treats a deny-lister error as a match (layers.go). There is no
//	branch here that turns an unreadable input into a pass.
//
//	The pattern grammar is `*` and `?` over the WHOLE command string, with
//	no path-separator semantics. filepath.Match is deliberately NOT used:
//	its `*` cannot cross a `/`, so `rm *` there would silently fail to
//	match `rm -rf /home` and a deny-list entry that under-matches fails
//	OPEN. Character classes are rejected rather than half-implemented.
//
// SPORT: internal/policy DenyListEngine/ADDED, DenyRule/ADDED,
//
//	DefaultDenyList/ADDED (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The stable identifier strings, per R-14.152: the pkg/cascade taxonomy is
// frozen at fourteen kinds, so a contract-named code survives as a string
// on the error rather than as a kind.
const (
	// CodeDenyListPatternInvalid marks a pattern the grammar rejects.
	CodeDenyListPatternInvalid = "denylist-pattern-invalid"
	// CodeDenyListStoreError marks a deny-list that could not be read or
	// written, or a row that would not decode.
	CodeDenyListStoreError = "denylist-store-error"
)

// ErrDenyListPatternInvalid is the comparison target for a refused
// pattern. It reuses this package's Code-comparing refusal type for the
// reason layers.go states for ErrDataClassDenied: the taxonomy error's own
// Is compares kinds alone, and several refusals here share a kind.
var ErrDenyListPatternInvalid = &ClassifyError{
	Code:  CodeDenyListPatternInvalid,
	Cause: cascade.New(cascade.KindInvalidInput, CodeDenyListPatternInvalid),
}

// ErrDenyListStoreError is the comparison target for an unreadable
// deny-list. A caller that sees it has been told the list could not be
// consulted, which layer 1 treats exactly as a match.
var ErrDenyListStoreError = &ClassifyError{
	Code:  CodeDenyListStoreError,
	Cause: cascade.New(cascade.KindInternal, CodeDenyListStoreError),
}

// newPatternInvalid builds a refusal for a pattern the grammar rejects.
func newPatternInvalid(pattern, reason string) *ClassifyError {
	return &ClassifyError{
		Code: ErrDenyListPatternInvalid.Code,
		Cause: cascade.Newf(cascade.KindInvalidInput,
			"policy: %s: the deny-list pattern %s is refused: %s",
			CodeDenyListPatternInvalid, quoteName(sanitize(pattern)), reason),
	}
}

// newStoreError builds a refusal for a deny-list that could not be
// consulted, wrapping the underlying cause when there is one.
func newStoreError(cause error, detail string) *ClassifyError {
	msg := "policy: " + CodeDenyListStoreError + ": " + detail
	if cause == nil {
		return &ClassifyError{
			Code:  ErrDenyListStoreError.Code,
			Cause: cascade.New(cascade.KindInternal, msg),
		}
	}
	return &ClassifyError{
		Code:  ErrDenyListStoreError.Code,
		Cause: cascade.Wrapf(cascade.KindInternal, cause, "%s", msg),
	}
}

// DenyRule is one deny-list row: a pattern, and the action class it is
// scoped to (R-21.233). The ZERO class is the wildcard and means every
// class; it is deliberately not run through safeActionClass, which would
// read it as destructive_privileged and narrow a row the operator wrote
// to mean "always".
type DenyRule struct {
	// Pattern is the `*`/`?` glob matched against a normalized command
	// form. It is never empty in a stored row.
	Pattern string `json:"pattern"`
	// Class scopes the row for ContainsClass. The zero value means all
	// classes; any other value must be one of the five.
	Class ActionClass `json:"class,omitempty"`
}

// anyActionClass is the wildcard class a row carries when the operator
// scoped it to nothing in particular. It is a var rather than a const
// because it is NOT a member of the ActionClass enum — it is the absence
// of one, and declaring it as a constant would enlist it in every
// exhaustive switch over the five real classes.
var anyActionClass = ActionClass(0)

// Validate refuses a row the matcher could not honour. It is the single
// gate every write and every decoded read passes through, so a row that
// reached storage by another route still cannot be matched against.
func (r DenyRule) Validate() error {
	if err := validateDenyPattern(r.Pattern); err != nil {
		return err
	}
	if r.Class != anyActionClass && !r.Class.Valid() {
		return newPatternInvalid(r.Pattern,
			"the row is scoped to an action class that does not exist")
	}
	return nil
}

// scopedTo reports whether this row answers a question about class. A
// wildcard row answers for every class; a scoped row answers only for its
// own.
func (r DenyRule) scopedTo(class ActionClass) bool {
	return r.Class == anyActionClass || r.Class == class
}

// DenyListEngine is the configurable portion of layer 1. It embeds the
// DenyLister seam layer 1 and the approval queue already consume, so an
// engine is usable everywhere a lister is, and adds the operator surface
// and the class-keyed query H/S-16.T3's standing-grant guard needs.
//
// The contract for this ticket names the matching method Match; the tree
// had already frozen it as DenyLister.Denied before this ticket landed,
// and a second matching method would be a second answer to one question.
// Denied IS Match.
type DenyListEngine interface {
	DenyLister

	// Add stores pattern scoped to class. A zero class means all classes.
	// A pattern the grammar rejects returns CodeDenyListPatternInvalid and
	// is not stored.
	Add(ctx context.Context, pattern string, class ActionClass) error

	// Remove deletes the row for pattern. Removing an absent pattern is
	// not an error.
	Remove(ctx context.Context, pattern string) error

	// List returns every row, in a deterministic order.
	List(ctx context.Context) ([]DenyRule, error)

	// ContainsClass reports whether action is listed by a row scoped to
	// class or by a wildcard row. It is a second QUERY SHAPE over the same
	// rows Denied matches, never a second store.
	ContainsClass(ctx context.Context, class ActionClass, action string) (bool, error)
}

// noDenyList is the complete default: an engine with no rows. It is not a
// placeholder (Art.1) — an operator who has configured nothing genuinely
// has an empty configurable list, and the unconditional §5.15 portion in
// layers.go applies underneath it either way.
type noDenyList struct{}

// DefaultDenyList returns the engine an Engine is built with: no
// configured rows, so the configurable layer adds nothing to the
// unconditional one. Writes to it are refused rather than silently
// dropped, because a caller that thinks it stored a deny-list entry and
// did not is exactly the failure this package exists to prevent.
func DefaultDenyList() DenyListEngine { return noDenyList{} }

// Denied implements DenyLister: nothing is configured, so nothing matches.
func (noDenyList) Denied(context.Context, string) (bool, error) { return false, nil }

// Add refuses: this engine has nowhere to put a row.
func (noDenyList) Add(_ context.Context, pattern string, _ ActionClass) error {
	return newStoreError(nil,
		"no deny-list store is configured, so "+quoteName(sanitize(pattern))+" cannot be stored")
}

// Remove is a no-op: there is no row to delete.
func (noDenyList) Remove(context.Context, string) error { return nil }

// List returns the empty list.
func (noDenyList) List(context.Context) ([]DenyRule, error) { return nil, nil }

// ContainsClass reports false for every pair, consistent with Denied.
func (noDenyList) ContainsClass(context.Context, ActionClass, string) (bool, error) {
	return false, nil
}

// validateDenyPattern is the pattern grammar, and the only place it is
// stated. It runs at write AND at match, so a row that reached storage
// without passing this check refuses at match time instead of being
// walked past.
func validateDenyPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return newPatternInvalid(pattern, "it is empty")
	}
	if pattern != strings.TrimSpace(pattern) {
		return newPatternInvalid(pattern,
			"it is padded with whitespace, which would never match a normalized command")
	}
	if strings.ContainsAny(pattern, "[]") {
		return newPatternInvalid(pattern,
			"character classes are not part of the deny-list grammar")
	}
	if strings.ContainsRune(pattern, '\\') {
		return newPatternInvalid(pattern,
			"escaping is not part of the deny-list grammar; the normalizer has already unescaped the command")
	}
	for _, r := range pattern {
		if r < 0x20 || r == 0x7f {
			return newPatternInvalid(pattern, "it contains a control character")
		}
	}
	return nil
}

// globMatch reports whether pattern matches s under the `*`/`?` grammar,
// with no path-separator semantics. When prefixOK is set it ALSO reports
// true whenever pattern could match some string that s is a prefix of:
// that is how a partially resolved command form fails closed, since the
// part the normalizer could not read might complete the pattern.
//
// Comparison is byte-wise, so `?` matches one byte rather than one rune.
// Deny-list patterns describe argv, which is ASCII in every form the
// classifier models.
func globMatch(pattern, s string, prefixOK bool) bool {
	var p, i, mark int
	star := -1
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case star >= 0:
			mark++
			p, i = star+1, mark
		default:
			// s cannot be consumed, and extending s cannot undo that.
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	if p == len(pattern) {
		return true
	}
	// Pattern text is left over. Only an unread tail could supply it.
	return prefixOK
}
