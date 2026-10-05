// Purpose: the one canonical data-sensitivity type (contract:sensitivity-tier).
//   Every vocabulary-1 sensitivity declaration in the tree (egress, process,
//   cascade-pa, nself) is an alias of SensitivityTier, so there is exactly
//   one parse, one name table and one restrictiveness ranking.
// Inputs: a tier name from a flag, a wire field or a stored row, or a legacy
//   JSON number written before the type had a text form.
// Outputs: a SensitivityTier, its text form, and the most restrictive join
//   of several tiers.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2).
//   SensitivityTier's zero value MUST read as SensitivityRestricted
//   (R-21.264; 06-FORGE-SPEC.md §5.16's fail-closed rule: "unset, unknown,
//   or unresolvable inherit => restricted") even though the normative
//   ordering local-only > restricted > internal > public ranks
//   SensitivityLocalOnly as textually more restrictive - local-only names
//   an explicit, narrow placement (controller machine only) that must
//   never be a silent default, so it is declared after the zero value.
//   Parsing is CLOSED: only the four exact names parse; "" is the zero
//   value; anything else returns SensitivityLocalOnly WITH a
//   KindInvalidInput error, so a boundary that ignores the error still
//   holds the narrowest tier. A JSON-decoded record with a bad tier is
//   KindIntegrity (a corrupt record, not caller input). Numeric values
//   and declaration order are frozen (persisted rows and legacy JSON
//   carry them).
// SPORT: pkg.provider.sensitivity-tier/CHANGE (contract:sensitivity-tier).

package provider

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SensitivityTier is the normative data-sensitivity enum a ModelRequest
// carries (06-FORGE-SPEC.md §5.16, ordered most to least restrictive:
// local-only > restricted > internal > public). The zero value is
// SensitivityRestricted, not SensitivityLocalOnly, so an unset field fails
// closed to "restricted" rather than silently to the even-narrower
// local-only placement (see this file's header comment).
type SensitivityTier uint8

// The four SensitivityTier members. Declaration order fixes each member's
// numeric value; sensitivityRank (below) carries the separate normative
// restrictiveness ordering, which does not follow this declaration order.
const (
	// SensitivityRestricted is the zero value and the fail-closed default.
	SensitivityRestricted SensitivityTier = iota
	// SensitivityLocalOnly never leaves the controller machine: no
	// external lane, no bridge, no node dispatch, no sync beyond the
	// owning device. Textually the most restrictive member, but never the
	// default (it requires an explicit caller opt-in).
	SensitivityLocalOnly
	// SensitivityInternal permits normal routing across configured lanes
	// with no public exposure.
	SensitivityInternal
	// SensitivityPublic carries no confidentiality constraint.
	SensitivityPublic
)

// sensitivityNames is indexed by SensitivityTier value; String uses it in
// place of a switch so the exhaustive linter never applies here.
var sensitivityNames = [...]string{"restricted", "local-only", "internal", "public"}

// sensitivityRank carries the normative restrictiveness ordering (local-only
// > restricted > internal > public), independent of each member's
// declaration-order numeric value. Higher ranks are more restrictive.
var sensitivityRank = [...]int{2, 3, 1, 0}

// Valid reports whether t is one of the four declared SensitivityTier
// members.
func (t SensitivityTier) Valid() bool {
	return t <= SensitivityPublic
}

// String returns the tier's stable lowercase-hyphenated name, or
// "invalid-sensitivity-tier" for a value outside the declared set.
func (t SensitivityTier) String() string {
	if !t.Valid() {
		return "invalid-sensitivity-tier"
	}
	return sensitivityNames[t]
}

// MoreRestrictiveThan reports whether t is strictly more restrictive than
// other under the normative ordering local-only > restricted > internal >
// public - the ranking a caller uses to decide whether resolving "inherit"
// or narrowing a tier is ever a widening (a LOOSENING per §5.14, forbidden
// outside an elevation flow). An invalid tier ranks as maximally
// restrictive, so a corrupt value never silently reads as permissive.
func (t SensitivityTier) MoreRestrictiveThan(other SensitivityTier) bool {
	return t.rank() > other.rank()
}

// rank returns t's normative restrictiveness rank (higher = more
// restrictive), defaulting invalid values to the most restrictive rank.
func (t SensitivityTier) rank() int {
	if !t.Valid() {
		return len(sensitivityRank)
	}
	return sensitivityRank[t]
}

// ParseSensitivityTier is the one closed parser for a tier name. It
// accepts exactly "local-only", "restricted", "internal" and "public"; the
// empty string is the documented zero value (SensitivityRestricted, nil).
// Any other value - a different case, surrounding whitespace, an
// underscore spelling, a vocabulary-2 word such as "secret" or a nodes
// word such as "normal" - returns SensitivityLocalOnly together with a
// KindInvalidInput error naming the value, so a caller that drops the
// error still holds the narrowest tier rather than a guessed one.
func ParseSensitivityTier(s string) (SensitivityTier, error) {
	if s == "" {
		return SensitivityRestricted, nil
	}
	for i, name := range sensitivityNames {
		if name == s {
			return SensitivityTier(i), nil
		}
	}
	return SensitivityLocalOnly, cascade.Newf(cascade.KindInvalidInput,
		"sensitivity: %q is not a tier (valid: local-only, restricted, internal, public)", s)
}

// MarshalText implements encoding.TextMarshaler: the tier's name. A value
// outside the declared set is refused with KindInvalidInput rather than
// written under a plausible spelling that would read back as a real tier.
func (t SensitivityTier) MarshalText() ([]byte, error) {
	if !t.Valid() {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"sensitivity: value %d is not a tier", uint8(t))
	}
	return []byte(sensitivityNames[t]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler through
// ParseSensitivityTier. On error *t is SensitivityLocalOnly, never the
// zero value, so a decoder that keeps going holds the narrowest tier.
func (t *SensitivityTier) UnmarshalText(b []byte) error {
	v, err := ParseSensitivityTier(string(b))
	*t = v
	return err
}

// UnmarshalJSON accepts the text form (a JSON string, via UnmarshalText)
// and the legacy numeric form written before SensitivityTier had a text
// encoding (a JSON number 0..3, the frozen member values). JSON is how
// stored and locally written records (ModelRequest, LegResult, sync
// records) come back, so an unknown name, any other number, a fraction or
// another JSON type is a corrupt record: *t becomes SensitivityLocalOnly
// and the error is KindIntegrity naming the value. JSON null leaves *t
// unchanged, as encoding/json does for every other type.
func (t *SensitivityTier) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			*t = SensitivityLocalOnly
			return cascade.Wrap(cascade.KindIntegrity, err, "sensitivity: malformed JSON string")
		}
		if err := t.UnmarshalText([]byte(s)); err != nil {
			return cascade.Wrap(cascade.KindIntegrity, err, "sensitivity: stored record")
		}
		return nil
	}
	n, err := strconv.ParseUint(string(b), 10, 8)
	if err != nil || !SensitivityTier(n).Valid() {
		*t = SensitivityLocalOnly
		return cascade.Newf(cascade.KindIntegrity,
			"sensitivity: JSON value %s is not a tier (legacy numbers are 0..3)", strconv.Quote(string(b)))
	}
	*t = SensitivityTier(n)
	return nil
}

// JoinSensitivity returns the most restrictive of ts under the normative
// ranking (local-only > restricted > internal > public). An invalid member
// joins as SensitivityLocalOnly, and the empty input - nothing classified
// at all - is SensitivityLocalOnly too, so the join can never be widened
// by omission or by a corrupt member.
func JoinSensitivity(ts ...SensitivityTier) SensitivityTier {
	if len(ts) == 0 {
		return SensitivityLocalOnly
	}
	out := SensitivityPublic
	for _, t := range ts {
		if !t.Valid() {
			return SensitivityLocalOnly
		}
		if t.MoreRestrictiveThan(out) {
			out = t
		}
	}
	return out
}
