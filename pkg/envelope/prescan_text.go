// Purpose: strict lexical checks for the wire scalars whose decoders are
//
//	more permissive than their encoders: timestamps, tier names and
//	numbers, plus clip for quoting untrusted text in error messages.
//
// Inputs: the raw bytes of one JSON string or number token.
// Outputs: nil, or a KindInvalidInput error.
// Constraints: Go's time parser accepts forms (a one-digit hour, a comma
//
//	fraction) that re-encode longer, and the tier decoder maps "" to
//	"restricted"; accepting only the encoder's own forms keeps a decoded
//	envelope's re-encoding no longer than its input. Error text quotes at
//	most clipLen bytes of input, so a refusal never allocates in
//	proportion to the input.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import "github.com/acamarata/cascade/pkg/provider"

// clipLen is the most input text an error message quotes.
const clipLen = 48

// maxNumberLen is the longest JSON number an int64 or uint64 field holds.
const maxNumberLen = 20

// clip returns at most clipLen bytes of v for an error message.
func clip[T ~string | ~[]byte](v T) string {
	if len(v) > clipLen {
		return string(v[:clipLen]) + "..."
	}
	return string(v)
}

// tierNames are the only tier spellings the wire accepts.
var tierNames = []string{
	provider.SensitivityRestricted.String(), provider.SensitivityLocalOnly.String(),
	provider.SensitivityInternal.String(), provider.SensitivityPublic.String(),
}

// checkTier refuses anything but an exact tier name ("" included).
func checkTier(b []byte) error {
	for _, n := range tierNames {
		if string(b) == n {
			return nil
		}
	}
	return invalidf("sensitivity %q is not a tier name", clip(b))
}

// checkTime accepts only YYYY-MM-DDTHH:MM:SS[.1-9 digits](Z|+HH:MM|-HH:MM).
func checkTime(b []byte) error {
	const layout = "dddd-dd-ddTdd:dd:dd"
	ok := len(b) >= len(layout)+1
	for i := 0; ok && i < len(layout); i++ {
		ok = layout[i] == 'd' && isDigit(b[i]) || layout[i] != 'd' && b[i] == layout[i]
	}
	rest := b[min(len(b), len(layout)):]
	if ok && rest[0] == '.' {
		n := 1
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		ok = n >= 2 && n <= 10
		rest = rest[n:]
	}
	ok = ok && (string(rest) == "Z" || len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') &&
		isDigit(rest[1]) && isDigit(rest[2]) && rest[3] == ':' && isDigit(rest[4]) && isDigit(rest[5]))
	if !ok {
		return invalidf("time %q is not RFC 3339", clip(b))
	}
	return nil
}

// checkNumber refuses an over-long number token.
func checkNumber(b []byte) error {
	if len(b) > maxNumberLen {
		return invalidf("number %q is too long", clip(b))
	}
	return nil
}

// isDigit reports an ASCII digit.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
