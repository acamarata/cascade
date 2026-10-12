// Purpose: the text rules validate shares between issuing and decoding:
//
//	every envelope string is valid UTF-8, and extension data obeys the
//	same key and depth rules Decode enforces on the wire.
//
// Inputs: an Envelope.
// Outputs: nil, or a KindInvalidInput error.
// Constraints: json.Marshal replaces invalid UTF-8 with U+FFFD, so two
//
//	envelopes differing only in invalid bytes would share one signature;
//	refusing them keeps Canonical one-to-one. Running the decoder's own
//	scanner over extension data means Issue never signs data Decode
//	refuses.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"encoding/json"
	"unicode/utf8"
)

// extDataDepth is the nesting depth of an extension's data in the wire
// form: Signed, env, ext list, extension object, data.
const extDataDepth = 4

// validateText refuses invalid UTF-8 anywhere and extension data the
// decoder would refuse.
func validateText(e Envelope) error {
	strs := []string{e.ID, e.ParentDigest, e.Issuer, e.Principal, e.Fence.DispatchID}
	for _, l := range [][]string{e.Roots, e.Tools, e.Commands, e.Egress} {
		strs = append(strs, l...)
	}
	for _, g := range e.Grants {
		strs = append(strs, g.ID, g.Verb)
	}
	for _, x := range e.Ext {
		strs = append(strs, x.Kind)
	}
	for _, s := range strs {
		if !utf8.ValidString(s) {
			return invalidf("string %q is not valid UTF-8", clip(s))
		}
	}
	for _, x := range e.Ext {
		if err := validateExtData(x); err != nil {
			return err
		}
	}
	return nil
}

// validateExtData requires valid JSON in valid UTF-8 that passes the
// decoder's structural scan at its wire depth.
func validateExtData(x Extension) error {
	if !utf8.Valid(x.Data) || !json.Valid(x.Data) {
		return invalidf("ext: %q data is not valid JSON in UTF-8", clip(x.Kind))
	}
	s := &scanner{b: x.Data}
	if err := s.value(shOpaque, extDataDepth); err != nil {
		return err
	}
	return nil
}

// compactInPlace removes insignificant whitespace from valid JSON b,
// reusing b's storage, and returns the shortened slice.
func compactInPlace(b []byte) []byte {
	w, inStr, esc := 0, false, false
	for _, c := range b {
		switch {
		case inStr:
			inStr = esc || c != '"'
			esc = !esc && c == '\\'
		case c == '"':
			inStr = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		}
		b[w] = c
		w++
	}
	return b[:w]
}
