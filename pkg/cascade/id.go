// Package cascade (id.go): Purpose: the module-wide identifier type. One
//
//	26-character Crockford-base32 rendering of 128 random bits, minted from
//	crypto/rand, used wherever a request, grant or execution needs a name
//	that no caller can guess and no two mints can collide on.
//
// Inputs: crypto/rand only. NewID reads no clock and no configuration, so
//
//	an identifier carries no timestamp a reader could mine.
//
// Outputs: ID, NewID, ParseID, ID.Valid, ID.String.
// Constraints: R-16.47 forbids a UUID dependency anywhere in core, so this
//
//	is the ONE identifier mint. ParseID is FAIL CLOSED: it accepts only the
//	exact 26-character alphabet below and refuses everything else, so a
//	value that reached memory by decoding untrusted bytes cannot present as
//	an identifier. The alphabet excludes I, L, O and U, which is what makes
//	a transcribed id unambiguous. SDK intent: this type is part of the
//	public pkg/cascade surface and is the identifier every SDK sees.
//
// SPORT: pkg/cascade ID/ADDED, NewID/ADDED, ParseID/ADDED
//
//	(P1-E08-W2-S16-T3).
package cascade

import (
	cryptorand "crypto/rand"
)

// idAlphabet is Crockford base32: the digits and the upper-case letters
// with I, L, O and U removed.
const idAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IDLength is the number of characters in every ID. 26 characters of
// base32 hold 130 bits, of which the low 128 are the random payload.
const IDLength = 26

// ID names one entity: a request, a grant, an execution. The zero value is
// deliberately not a valid ID, so a struct field left unset refuses at
// ParseID rather than reading as a real name.
type ID string

// String renders the identifier.
func (i ID) String() string { return string(i) }

// Valid reports whether i is exactly IDLength characters drawn from the
// Crockford alphabet. It is the only definition of well-formed, and both
// ParseID and every caller that receives an ID from outside go through it.
func (i ID) Valid() bool {
	if len(i) != IDLength {
		return false
	}
	for j := 0; j < len(i); j++ {
		if !isIDChar(i[j]) {
			return false
		}
	}
	return true
}

// isIDChar reports whether c is in the Crockford alphabet. The membership
// test is an explicit scan of the alphabet rather than a range check, so a
// character the alphabet excludes (I, L, O, U) cannot be admitted by an
// off-by-one in a range.
func isIDChar(c byte) bool {
	for k := 0; k < len(idAlphabet); k++ {
		if idAlphabet[k] == c {
			return true
		}
	}
	return false
}

// NewID mints a fresh identifier from 128 bits of crypto/rand. An error
// from the entropy source is returned rather than swallowed: a caller that
// cannot get randomness must refuse, not carry on with a predictable name.
func NewID() (ID, error) {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", Wrap(KindInternal, err, "cascade: minting an identifier")
	}
	out := make([]byte, IDLength)
	var acc uint32
	var bits uint
	pos := IDLength - 1
	for i := len(raw) - 1; i >= 0; i-- {
		acc |= uint32(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = idAlphabet[acc&0x1f]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	out[pos] = idAlphabet[acc&0x1f]
	return ID(out), nil
}

// ParseID converts an untrusted string into an ID, refusing anything that
// is not well-formed. This is the fail-closed door: decoded bytes, CLI
// arguments and bridge payloads all arrive as strings, and none of them
// becomes an ID without passing here.
func ParseID(s string) (ID, error) {
	id := ID(s)
	if !id.Valid() {
		return "", Newf(KindInvalidInput,
			"cascade: %d-character identifier expected, got a value that is not one", IDLength)
	}
	return id, nil
}
