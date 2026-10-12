// Purpose: the byte-level walk behind prescan: values, objects, arrays,
//
//	strings and scalars, by index and without allocation.
//
// Inputs: the scanner's input bytes.
// Outputs: an error for the first structural rule broken; the scanner's
//
//	grow counter for the bytes re-escaping can add.
//
// Constraints: every loop advances the index or returns, so malformed
//
//	input ends the walk; key tracking for extension-data objects uses a
//	fixed array on the stack.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import "bytes"

// span is the byte range of one object key.
type span struct{ start, end int }

// value scans one JSON value expected to have shape sh.
func (s *scanner) value(sh shape, depth int) error {
	if depth > maxDepth {
		return invalidf("nesting deeper than %d", maxDepth)
	}
	s.ws()
	if s.i >= len(s.b) {
		return nil // truncated input: the decoder reports it
	}
	if err := s.expect(sh); err != nil {
		return err
	}
	switch s.b[s.i] {
	case '{':
		return s.object(sh, depth)
	case '[':
		return s.array(sh, depth)
	case '"':
		start, end, err := s.str()
		if err != nil {
			return err
		}
		return checkText(sh, s.b[start:end])
	}
	start := s.i
	if err := s.scalar(); err != nil {
		return err
	}
	if sh == shNum {
		return checkNumber(s.b[start:s.i])
	}
	return nil
}

// checkText applies the strict lexical rule of a string shape.
func checkText(sh shape, b []byte) error {
	if sh == shTime {
		return checkTime(b)
	}
	if sh == shTier {
		return checkTier(b)
	}
	return nil
}

// object scans an object. Every object refuses escaped keys and keys
// equal to an earlier key up to case folding; a struct shape also
// refuses unknown fields and requires its non-optional fields.
func (s *scanner) object(sh shape, depth int) error {
	s.i++ // '{'
	known := keys[sh]
	var seen uint64
	var prev [maxObjectKeys]span
	n := 0
	for {
		s.ws()
		if s.i >= len(s.b) || s.b[s.i] == '}' {
			s.i++
			if known != nil && seen&required(known) != required(known) {
				return invalidf("missing field %q", missing(known, seen))
			}
			return nil
		}
		if s.b[s.i] == ',' {
			s.i++
			continue
		}
		start, end, err := s.str()
		if err != nil {
			return err
		}
		key, quoted := s.b[start:end], s.b[start-1:end+1]
		if err := plainKey(key, quoted); err != nil {
			return err
		}
		child := shOpaque
		if known != nil {
			var bit uint64
			if child, bit, err = classify(known, key, quoted); err != nil {
				return err
			}
			if seen&bit != 0 {
				return invalidf("duplicate key %s", clip(quoted))
			}
			seen |= bit
		} else if err := s.track(&prev, &n, span{start, end}); err != nil {
			return err
		}
		s.ws()
		if s.i < len(s.b) && s.b[s.i] == ':' {
			s.i++
		}
		if err := s.value(child, depth+1); err != nil {
			return err
		}
	}
}

// expect refuses a value whose JSON type does not match shape sh: an
// object for a struct shape, a string for shStr, a number for shNum.
func (s *scanner) expect(sh shape) error {
	c := s.b[s.i]
	switch {
	case keys[sh] != nil && c != '{':
		return invalidf("expected an object at byte %d", s.i)
	case (sh == shStr || sh == shTime || sh == shTier) && c != '"':
		return invalidf("expected a string at byte %d", s.i)
	case sh == shNum && c != '-' && (c < '0' || c > '9'):
		return invalidf("expected a number at byte %d", s.i)
	}
	return nil
}

// plainKey refuses a key json.Marshal would write differently: one with
// an escape, or with a raw <, >, &, U+2028 or U+2029 it re-escapes.
func plainKey(key, quoted []byte) error {
	if bytes.IndexByte(key, '\\') >= 0 {
		return invalidf("escaped key %s", clip(quoted))
	}
	if bytes.ContainsAny(key, "<>&\u2028\u2029") {
		return invalidf("key %s holds a character JSON re-escapes", clip(quoted))
	}
	return nil
}

// track records key k of an extension-data object, refusing a key equal
// to an earlier one up to case folding and more than maxObjectKeys keys.
func (s *scanner) track(prev *[maxObjectKeys]span, n *int, k span) error {
	key := s.b[k.start:k.end]
	if *n == maxObjectKeys {
		return invalidf("extension data object has more than %d keys", maxObjectKeys)
	}
	for _, p := range prev[:*n] {
		if bytes.EqualFold(s.b[p.start:p.end], key) {
			return invalidf("duplicate key %s in extension data", clip(s.b[k.start-1:k.end+1]))
		}
	}
	prev[*n] = k
	*n++
	return nil
}

// classify returns the value shape and seen-bit for key in a struct
// shape's table, refusing case-variant and unknown keys.
func classify(known []field, key, quoted []byte) (shape, uint64, error) {
	for i, f := range known {
		if bytes.Equal(key, f.key) {
			return f.value, 1 << uint(i), nil
		}
	}
	for _, f := range known {
		if bytes.EqualFold(key, f.key) {
			return 0, 0, invalidf("key %s is not the exact field name %q", clip(quoted), f.key)
		}
	}
	return 0, 0, invalidf("unknown field %s", clip(quoted))
}

// array scans an array, enforcing a list shape's limit.
func (s *scanner) array(sh shape, depth int) error {
	s.i++ // '['
	spec, isList := lists[sh]
	n := 0
	for {
		s.ws()
		if s.i >= len(s.b) || s.b[s.i] == ']' {
			s.i++
			return nil
		}
		if s.b[s.i] == ',' {
			s.i++
			continue
		}
		n++
		if isList && n > spec.max {
			return invalidf("%s: more than %d entries", spec.name, spec.max)
		}
		if err := s.value(spec.elem, depth+1); err != nil {
			return err
		}
	}
}

// str scans a string starting at the opening quote and returns the byte
// range of its contents. It adds to grow what json.Marshal's escaping can
// add: 5 bytes for each raw <, > or &, and 3 for each raw U+2028 or
// U+2029 (Go 1.22 and later keep \b and \f short, so escapes never grow).
func (s *scanner) str() (start, end int, err error) {
	s.i++ // '"'
	start = s.i
	for s.i < len(s.b) {
		switch c := s.b[s.i]; {
		case c == '\\':
			s.i += 2
		case c == '"':
			end = s.i
			s.i++
			return start, end, nil
		case c < 0x20:
			return 0, 0, invalidf("control byte in string")
		case c == '<' || c == '>' || c == '&':
			s.grow += 5
			s.i++
		case c == 0xE2 && s.i+2 < len(s.b) && s.b[s.i+1] == 0x80 && (s.b[s.i+2] == 0xA8 || s.b[s.i+2] == 0xA9):
			s.grow += 3
			s.i += 3
		default:
			s.i++
		}
	}
	return 0, 0, invalidf("unterminated string")
}

// scalar scans a number or literal; any other byte is malformed.
func (s *scanner) scalar() error {
	start := s.i
	for s.i < len(s.b) && isScalarByte(s.b[s.i]) {
		s.i++
	}
	if s.i == start {
		return invalidf("unexpected byte %q", s.b[s.i])
	}
	return nil
}

// isScalarByte reports bytes that may appear in a JSON number or literal.
func isScalarByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '+' || c == '-' || c == '.'
}

// ws skips JSON whitespace.
func (s *scanner) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}
