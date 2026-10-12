// Purpose: the canonical byte form of an envelope (what the controller
//
//	signs) and its digest, plus the normalisation rules both enforce.
//
// Inputs: an Envelope.
// Outputs: domain separator + JSON bytes, or the hex sha256 of them.
// Constraints: non-normalised input (unsorted or duplicate list entries,
//
//	duplicate extension kinds, a root that is not absolute and clean, an
//	invalid tier, a negative budget, a wrong version) is refused with
//	KindInvalidInput, never silently repaired, so one envelope has exactly
//	one encoding. Times are encoded in UTC.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Canonical returns the bytes the controller signs: the domain separator
// followed by the JSON of e with its times in UTC. A non-normalised
// envelope returns a KindInvalidInput error.
func Canonical(e Envelope) ([]byte, error) {
	if err := validate(e); err != nil {
		return nil, err
	}
	n := utcTimes(e)
	body, err := json.Marshal(n)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "envelope: encode canonical form")
	}
	if len(body) > maxCanonicalBody {
		return nil, invalidf("canonical form is %d bytes, over %d", len(body), maxCanonicalBody)
	}
	out := make([]byte, 0, len(domainSeparator)+len(body))
	out = append(out, domainSeparator...)
	return append(out, body...), nil
}

// maxCanonicalBody is the largest envelope JSON whose Signed wire form,
// {"env":BODY,"sig":"<88 base64 bytes>"}, still fits MaxEncodedBytes, so
// every envelope Issue signs can be decoded by its peer.
const maxCanonicalBody = MaxEncodedBytes - len(`{"env":,"sig":""}`) - 88

// Digest returns hex(sha256(Canonical(e))), the value a child envelope
// carries as ParentDigest.
func Digest(e Envelope) (string, error) {
	c, err := Canonical(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// utcTimes returns a copy of e with every time in UTC. Slices are copied
// so the caller's envelope is never mutated.
func utcTimes(e Envelope) Envelope {
	e.IssuedAt = e.IssuedAt.UTC()
	e.ExpiresAt = e.ExpiresAt.UTC()
	if e.Grants != nil {
		g := make([]GrantRef, len(e.Grants))
		for i, r := range e.Grants {
			r.ExpiresAt = r.ExpiresAt.UTC()
			g[i] = r
		}
		e.Grants = g
	}
	return e
}

// validate enforces the normalisation and range rules.
func validate(e Envelope) error {
	if e.Version != Version {
		return invalidf("version %d is not %d", e.Version, Version)
	}
	if !e.Sensitivity.Valid() {
		return invalidf("sensitivity tier %d is not a declared tier", e.Sensitivity)
	}
	b := e.Budget
	if b.WallClock < 0 || b.Tokens < 0 || b.CostMicros < 0 || b.Procs < 0 {
		return invalidf("budget components must not be negative")
	}
	if err := validateLists(e); err != nil {
		return err
	}
	if err := validateRefs(e); err != nil {
		return err
	}
	if err := validateTimes(e); err != nil {
		return err
	}
	if err := validateText(e); err != nil {
		return err
	}
	for _, r := range e.Roots {
		if err := validRoot(r); err != nil {
			return err
		}
	}
	return nil
}

// validateLists checks every list's length, order and uniqueness.
func validateLists(e Envelope) error {
	lists := []struct {
		field string
		vals  []string
		max   int
	}{
		{"roots", e.Roots, maxRoots},
		{"tools", e.Tools, maxNames},
		{"cmds", e.Commands, maxNames},
		{"egress", e.Egress, maxNames},
	}
	for _, l := range lists {
		if err := sortedUnique(l.field, l.vals, l.max); err != nil {
			return err
		}
	}
	return nil
}

// validateRefs checks grants and extensions: limit, non-empty keys, order
// and uniqueness (one extension per kind).
func validateRefs(e Envelope) error {
	if len(e.Grants) > maxRefs {
		return invalidf("grants: more than %d entries", maxRefs)
	}
	for _, g := range e.Grants {
		if g.ID == "" || g.Verb == "" {
			return invalidf("grants: empty id or verb")
		}
	}
	for i := 1; i < len(e.Grants); i++ {
		a, b := e.Grants[i-1], e.Grants[i]
		if a.ID > b.ID || (a.ID == b.ID && a.Verb >= b.Verb) {
			return invalidf("grants: entries are not sorted by (id, verb) and unique")
		}
	}
	if len(e.Ext) > maxRefs {
		return invalidf("ext: more than %d entries", maxRefs)
	}
	if len(e.Ext) > 0 && e.Ext[0].Kind == "" {
		return invalidf("ext: empty extension kind")
	}
	for i := 1; i < len(e.Ext); i++ {
		if e.Ext[i-1].Kind == e.Ext[i].Kind {
			return invalidf("ext: duplicate extension kind %q", clip(e.Ext[i].Kind))
		}
		if e.Ext[i-1].Kind > e.Ext[i].Kind {
			return invalidf("ext: entries are not sorted by kind")
		}
	}
	return nil
}

// validateTimes requires every time to encode in UTC as a four-digit
// year, so a decoded envelope always has a canonical form.
func validateTimes(e Envelope) error {
	ts := []time.Time{e.IssuedAt, e.ExpiresAt}
	for _, g := range e.Grants {
		ts = append(ts, g.ExpiresAt)
	}
	for _, t := range ts {
		if y := t.UTC().Year(); y < 0 || y > 9999 {
			return invalidf("time %s is outside years 0000-9999 in UTC", t)
		}
	}
	return nil
}

// sortedUnique refuses a list that is too long, unsorted or repeats an
// entry.
func sortedUnique(field string, vals []string, limit int) error {
	if len(vals) > limit {
		return invalidf("%s: more than %d entries", field, limit)
	}
	if len(vals) > 0 && vals[0] == "" {
		return invalidf("%s: empty entry", field)
	}
	if field != "roots" && contains(vals, wildcard) {
		return invalidf("%s: the wildcard %s is refused", field, wildcard)
	}
	for i := 1; i < len(vals); i++ {
		if vals[i-1] >= vals[i] {
			return invalidf("%s: entries are not sorted and unique", field)
		}
	}
	return nil
}

// validRoot requires an absolute, clean, slash-separated path with no NUL.
// path.Clean of an absolute path removes every "..", so a root that still
// differs from its Clean form (for example "/a/b/../../etc") is refused.
func validRoot(r string) error {
	if !path.IsAbs(r) || path.Clean(r) != r || strings.ContainsRune(r, 0) {
		return invalidf("roots: %q is not an absolute clean path", clip(r))
	}
	return nil
}

// invalidf builds a KindInvalidInput error with the package prefix.
func invalidf(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "envelope: "+format, args...)
}
