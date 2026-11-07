package executive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrCredentialInPayload reports a credential-named key or a credential
// value in a payload or a serialized public surface. Its text names only
// the depth, class or count, never the matched bytes.
var ErrCredentialInPayload = errors.New("credential in exec payload")

// forbiddenKeys is the exact set of normalized key names a payload may
// not carry. Matching is exact, never by substring.
var forbiddenKeys = map[string]struct{}{
	"credential": {}, "credentials": {}, "credentialref": {}, "secret": {}, "secretref": {},
	"apikey": {}, "token": {}, "accesstoken": {}, "refreshtoken": {}, "password": {},
	"authorization": {}, "privatekey": {}, "bearer": {},
}

// maxKeyDepth bounds the nesting CheckPayloadKeys walks.
const maxKeyDepth = 256

// redacted replaces every detected span in a sanitized error string.
const redacted = "[redacted]"

var keyStripper = strings.NewReplacer("_", "", "-", "", ".", "")

// normalize lower-cases name and removes "_", "-" and ".".
func normalize(name string) string {
	return keyStripper.Replace(strings.ToLower(name))
}

// isForbiddenKey reports whether name normalizes into the forbidden set.
func isForbiddenKey(name string) bool {
	_, bad := forbiddenKeys[normalize(name)]
	return bad
}

// CheckPayloadKeys walks every object key of raw at every depth, inside
// arrays too, and returns ErrCredentialInPayload for a key whose
// normalized form is in the forbidden set. Input that is not a sequence of
// well-formed JSON values is ErrInvalidExecPayload.
func CheckPayloadKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	for n := 0; ; n++ {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) && n > 0 {
			return nil
		}
		if err != nil {
			return errMalformedPayload()
		}
		if err := walkKeys(dec, tok, 0); err != nil {
			return err
		}
	}
}

// walkKeys consumes the value that starts with tok, checking every key.
func walkKeys(dec *json.Decoder, tok json.Token, depth int) error {
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxKeyDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrInvalidExecPayload, maxKeyDepth)
	}
	for dec.More() {
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return errMalformedPayload()
			}
			if name, _ := key.(string); isForbiddenKey(name) {
				return fmt.Errorf("%w: forbidden key at depth %d", ErrCredentialInPayload, depth+1)
			}
		}
		next, err := dec.Token()
		if err != nil {
			return errMalformedPayload()
		}
		if err := walkKeys(dec, next, depth+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return errMalformedPayload()
	}
	return nil
}

func errMalformedPayload() error {
	return fmt.Errorf("%w: malformed JSON", ErrInvalidExecPayload)
}

// GuardSerialized refuses serialized bytes b when det reports any certain
// credential hit. The error names the classes and the count only. A nil
// det is KindInvalidInput: the guard never runs open.
func GuardSerialized(det *secrets.Detector, b []byte) error {
	if det == nil {
		return cascade.New(cascade.KindInvalidInput, "executive: nil credential detector")
	}
	hits := det.ScanCertain(b)
	if len(hits) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d certain hit(s) of class %s", ErrCredentialInPayload, len(hits), hitClasses(hits))
}

// hitClasses lists the distinct classes of hits, sorted and comma-joined.
func hitClasses(hits []secrets.DetectionHit) string {
	seen := make(map[string]struct{}, len(hits))
	var classes []string
	for _, h := range hits {
		c := string(h.Class)
		if _, dup := seen[c]; !dup {
			seen[c] = struct{}{}
			classes = append(classes, c)
		}
	}
	sort.Strings(classes)
	return strings.Join(classes, ",")
}

// SanitizeError renders err with every span det.Scan reports replaced by
// "[redacted]". A nil err is ""; a nil det redacts the whole message.
func SanitizeError(det *secrets.Detector, err error) string {
	if err == nil {
		return ""
	}
	if det == nil {
		return redacted
	}
	msg := err.Error()
	var b strings.Builder
	last := 0
	for _, h := range det.Scan([]byte(msg)) {
		start := max(h.Offset, last)
		b.WriteString(msg[last:start])
		b.WriteString(redacted)
		last = max(last, h.Offset+h.Len)
	}
	b.WriteString(msg[last:])
	return b.String()
}
