// Purpose: an allocation-free structural pass over untrusted envelope bytes
//
//	that runs before encoding/json allocates the decoded value.
//
// Inputs: at most MaxEncodedBytes of valid UTF-8.
// Outputs: the number of bytes re-encoding can add (escapes), or a
//
//	KindInvalidInput error for nesting deeper than maxDepth, a list over
//	its limit, an escaped key, a duplicate or case-folded duplicate key in
//	any object, a key that only case-folds to a known field name, an
//	unknown or missing field in a known object, or trailing data.
//
// Constraints: encoding/json keeps the LAST of two duplicate keys and
//
//	matches keys case-insensitively (including Unicode folds such as the
//	Kelvin sign), so {"sig":..,"SIG":..} would decode silently; this pass
//	refuses both forms everywhere, extension data included. Every
//	non-omitempty field must be present, so re-encoding a decoded envelope
//	never adds a defaulted field and its size is bounded by the input size
//	plus the counted escape growth. Malformed JSON is left to the decoder.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

// maxDepth bounds JSON nesting anywhere in the input, including inside
// extension data.
const maxDepth = 32

// maxObjectKeys bounds the keys of one object inside extension data.
const maxObjectKeys = 64

// shape is the expected JSON shape of a value at one position.
type shape uint8

// The shapes. Struct shapes carry a key table, list shapes a listSpec;
// shStr and shNum pin a scalar's JSON type, so a null or a legacy number
// can never decode into a field that re-encodes longer.
const (
	shOpaque shape = iota
	shStr          // a JSON string
	shNum          // a JSON number
	shTime         // a JSON string in strict RFC 3339 form
	shTier         // a JSON string naming a sensitivity tier
	shSigned
	shEnv
	shBudget
	shFence
	shGrant
	shExt
	shRoots
	shTools
	shCmds
	shEgress
	shGrants
	shExts
)

// field is one known key of a struct shape, the shape of its value and
// whether it may be absent (an omitempty field).
type field struct {
	key      []byte
	value    shape
	optional bool
}

// listSpec is a list shape's field name, limit and element shape.
type listSpec struct {
	name string
	max  int
	elem shape
}

// keys lists each struct shape's known keys (at most 64 per shape).
var keys = map[shape][]field{
	shSigned: {{[]byte("env"), shEnv, false}, {[]byte("sig"), shStr, false}},
	shEnv: {
		{[]byte("v"), shNum, false}, {[]byte("id"), shStr, false}, {[]byte("parent"), shStr, true},
		{[]byte("iss"), shStr, false}, {[]byte("principal"), shStr, false}, {[]byte("roots"), shRoots, false},
		{[]byte("tools"), shTools, false}, {[]byte("cmds"), shCmds, false}, {[]byte("egress"), shEgress, false},
		{[]byte("grants"), shGrants, false}, {[]byte("budget"), shBudget, false}, {[]byte("ext"), shExts, true},
		{[]byte("fence"), shFence, false}, {[]byte("sens"), shTier, false}, {[]byte("iat"), shTime, false},
		{[]byte("exp"), shTime, false},
	},
	shBudget: {{[]byte("wall"), shNum, false}, {[]byte("tok"), shNum, false}, {[]byte("cost"), shNum, false}, {[]byte("procs"), shNum, false}},
	shFence:  {{[]byte("dispatch"), shStr, false}, {[]byte("attempt"), shNum, false}, {[]byte("epoch"), shNum, false}},
	shGrant:  {{[]byte("id"), shStr, false}, {[]byte("verb"), shStr, false}, {[]byte("exp"), shTime, false}},
	shExt:    {{[]byte("kind"), shStr, false}, {[]byte("data"), shOpaque, false}},
}

// lists maps each list shape to its limit.
var lists = map[shape]listSpec{
	shRoots:  {"roots", maxRoots, shStr},
	shTools:  {"tools", maxNames, shStr},
	shCmds:   {"cmds", maxNames, shStr},
	shEgress: {"egress", maxNames, shStr},
	shGrants: {"grants", maxRefs, shGrant},
	shExts:   {"ext", maxRefs, shExt},
}

// scanner walks b by index; it never allocates. grow counts the bytes
// json.Marshal adds when it re-escapes the decoded strings.
type scanner struct {
	b    []byte
	i    int
	grow int
}

// prescan runs the structural pass over b and returns the escape growth.
func prescan(b []byte) (int, error) {
	s := &scanner{b: b}
	if err := s.value(shSigned, 0); err != nil {
		return 0, err
	}
	s.ws()
	if s.i < len(s.b) {
		return 0, invalidf("trailing data after the envelope")
	}
	return s.grow, nil
}

// required returns the seen-bit mask of known's non-optional keys.
func required(known []field) uint64 {
	var m uint64
	for i, f := range known {
		if !f.optional {
			m |= 1 << uint(i)
		}
	}
	return m
}

// missing names the first required key absent from seen.
func missing(known []field, seen uint64) []byte {
	for i, f := range known {
		if !f.optional && seen&(1<<uint(i)) == 0 {
			return f.key
		}
	}
	return nil
}
