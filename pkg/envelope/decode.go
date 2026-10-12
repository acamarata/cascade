// Purpose: Decode, the bounded wire decoder for a Signed envelope.
//
// Inputs: untrusted bytes from a peer (a node dispatch frame, a store).
// Outputs: a Signed whose envelope is normalised, or a KindInvalidInput
//
//	error. Decode does not verify the signature; callers call Verify.
//
// Constraints: never panics; refuses a signature that is not 64 bytes,
//
//	input whose re-encoding could exceed MaxEncodedBytes (counting the
//	escapes json.Marshal adds), input over MaxEncodedBytes, invalid
//
//	UTF-8, unknown fields, duplicate or case-variant keys, nesting deeper
//	than maxDepth, over-long lists and trailing data BEFORE allocating the
//	decoded value (prescan.go), so the memory one call allocates stays
//	under 4 x MaxEncodedBytes. json.Unmarshal is used, not a json.Decoder
//	with DisallowUnknownFields: the Decoder's doubling read buffer alone
//	allocates about 255 KiB for a 64 KiB input; the prescan's exact key
//	table refuses unknown fields instead.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Decode parses one Signed envelope from b.
func Decode(b []byte) (Signed, error) {
	if len(b) > MaxEncodedBytes {
		return Signed{}, invalidf("encoded envelope is %d bytes, over %d", len(b), MaxEncodedBytes)
	}
	if !utf8.Valid(b) {
		return Signed{}, invalidf("encoded envelope is not valid UTF-8")
	}
	grow, err := prescan(b)
	if err != nil {
		return Signed{}, err
	}
	if len(b)+grow > MaxEncodedBytes {
		return Signed{}, invalidf("re-encoded envelope could reach %d bytes, over %d", len(b)+grow, MaxEncodedBytes)
	}
	var s Signed
	if err := json.Unmarshal(b, &s); err != nil {
		return Signed{}, cascade.Wrap(cascade.KindInvalidInput, err, "envelope: decode")
	}
	if len(s.Sig) != ed25519.SignatureSize {
		return Signed{}, invalidf("signature has length %d, want %d", len(s.Sig), ed25519.SignatureSize)
	}
	if err := validate(s.Env); err != nil {
		return Signed{}, err
	}
	s.Env = utcTimes(s.Env)
	for i := range s.Env.Ext {
		s.Env.Ext[i].Data = compactInPlace(s.Env.Ext[i].Data)
	}
	return s, nil
}
