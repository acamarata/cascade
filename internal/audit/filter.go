package audit

// Purpose: the query filter, its fields, the closed "key=value" token
//   parser a command line uses, fail-closed validation, matching, and the
//   opaque pagination cursor.
// Inputs: caller-supplied filter fields or tokens.
// Outputs: a validated Filter, or a pkg/cascade taxonomy error.
// Constraints: FAIL CLOSED everywhere. An unrecognised field, an
//   unparseable value, a malformed cursor, or a present-but-empty
//   constraint list REFUSES. None of them widens into "match everything":
//   a filter that cannot be honoured exactly as written must never answer
//   with more records than were asked for.
// SPORT: internal.audit.Log/ADDED (P1-E09-W2-S18-T2).

import (
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultLimit and maxLimit bound one page.
const (
	defaultLimit = 100
	maxLimit     = 1000
)

// cursorPrefix marks a cursor as this package's own, so a token from some
// other paginated surface is refused rather than half-parsed.
const cursorPrefix = "s"

// Filter selects records. A nil slice means "no constraint on this field".
// A non-nil but empty slice is REFUSED rather than treated as a wildcard.
type Filter struct {
	// Since bounds the range below, inclusive. Zero means unbounded.
	Since time.Time
	// Until bounds the range above, exclusive. Zero means unbounded.
	Until time.Time
	// Kinds, when non-nil, admits only these event kinds. Every entry
	// must be one of the eleven ratified kinds.
	Kinds []Kind
	// Actors, when non-nil, admits only these actors (exact match).
	Actors []string
	// Verdicts, when non-nil, admits only these verdicts (exact match).
	Verdicts []string
	// Limit caps the page. Zero means defaultLimit; negative is refused.
	Limit int
	// Cursor resumes a previous page. Empty starts at the oldest record.
	Cursor string
}

// ParseFilter builds a Filter from "key=value" tokens, which is the form a
// command line hands in. The key set is CLOSED: an unrecognised key is
// refused, never ignored. Ignoring it would silently drop a constraint the
// caller asked for and answer with a wider result set than was requested.
func ParseFilter(tokens []string) (Filter, error) {
	var f Filter
	for _, tok := range tokens {
		key, value, ok := strings.Cut(tok, "=")
		if !ok {
			return Filter{}, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter,
				"token %q is not key=value", tok)
		}
		if err := applyToken(&f, key, value); err != nil {
			return Filter{}, err
		}
	}
	return f, nil
}

// applyToken applies one parsed token to f.
func applyToken(f *Filter, key, value string) error {
	switch key {
	case "kind":
		f.Kinds = append(f.Kinds, Kind(value))
	case "actor":
		f.Actors = append(f.Actors, value)
	case "verdict":
		f.Verdicts = append(f.Verdicts, value)
	case "cursor":
		f.Cursor = value
	case "since", "until", "limit":
		return applyScalarToken(f, key, value)
	default:
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "unknown filter field %q", key)
	}
	return nil
}

// applyScalarToken parses the tokens whose values are not plain strings.
func applyScalarToken(f *Filter, key, value string) error {
	if key == "limit" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "limit %q is not a number", value)
		}
		f.Limit = n
		return nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter,
			"%s %q is not an RFC3339 timestamp", key, value)
	}
	if key == "since" {
		f.Since = ts
		return nil
	}
	f.Until = ts
	return nil
}

// validate refuses a filter that cannot be honoured exactly as written.
func (f Filter) validate() error {
	for _, k := range f.Kinds {
		if !k.Valid() {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter,
				"kind %q is not one of the ratified kinds", string(k))
		}
	}
	if err := checkList("kinds", len(f.Kinds), f.Kinds == nil); err != nil {
		return err
	}
	if err := checkStrings("actors", f.Actors); err != nil {
		return err
	}
	if err := checkStrings("verdicts", f.Verdicts); err != nil {
		return err
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter,
			"until is not after since")
	}
	if f.Limit < 0 || f.Limit > maxLimit {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "limit %d is out of range", f.Limit)
	}
	return nil
}

// checkList refuses a present-but-empty constraint list.
func checkList(name string, n int, isNil bool) error {
	if !isNil && n == 0 {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter,
			"%s is present but empty; omit the field to place no constraint on it", name)
	}
	return nil
}

// checkStrings refuses an empty list and any empty entry within one.
func checkStrings(name string, values []string) error {
	if err := checkList(name, len(values), values == nil); err != nil {
		return err
	}
	for _, v := range values {
		if v == "" {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "%s contains an empty entry", name)
		}
	}
	return nil
}

// matches reports whether rec satisfies every constraint in f.
func (f Filter) matches(rec Record) bool {
	ts := rec.Time()
	if !f.Since.IsZero() && ts.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !ts.Before(f.Until) {
		return false
	}
	if f.Kinds != nil && !containsKind(f.Kinds, rec.Kind) {
		return false
	}
	if f.Actors != nil && !containsString(f.Actors, rec.Actor) {
		return false
	}
	return f.Verdicts == nil || containsString(f.Verdicts, rec.Verdict)
}

func containsKind(list []Kind, want Kind) bool {
	for _, k := range list {
		if k == want {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// encodeCursor renders a resume token for the last sequence number read.
func encodeCursor(seq uint64) string { return cursorPrefix + strconv.FormatUint(seq, 10) }

// decodeCursor parses a resume token. An empty cursor is the start of the
// log; anything that is not this package's own token shape is refused.
func decodeCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	digits, ok := strings.CutPrefix(cursor, cursorPrefix)
	if !ok || digits == "" {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "cursor %q is malformed", cursor)
	}
	seq, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidFilter, "cursor %q is malformed", cursor)
	}
	return seq, nil
}
