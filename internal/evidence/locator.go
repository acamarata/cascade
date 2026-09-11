package evidence

// Purpose: Locator is the typed rendering of R-21.41's closed three-form
// enumeration ("lines a-b" | byte range | artifact ref), grammar fixed by
// R-21.54.
// Inputs: an untrusted locator string (ParseLocator).
// Outputs: a Locator, or a typed invalid-input error on any malformed
// input.
// Constraints: ParseLocator and Locator.String round-trip exactly. Being a
// parser, this file carries the 06 SS5 rule 7 fuzz target FuzzLocatorParse
// (fuzz_locator_test.go).
//
// SPORT: evidence/locator (ADD).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LocatorKind is the closed three-value form a Locator takes. The zero
// value is deliberately not a member.
type LocatorKind string

const (
	// LocatorLines addresses a 1-based, both-ends-inclusive line range.
	LocatorLines LocatorKind = "lines"
	// LocatorBytes addresses a 0-based, half-open [start,end) byte range.
	LocatorBytes LocatorKind = "bytes"
	// LocatorArtifact addresses a whole artifact by its store ref.
	LocatorArtifact LocatorKind = "artifact"
)

// Valid reports whether k is one of the three closed values.
func (k LocatorKind) Valid() bool {
	switch k {
	case LocatorLines, LocatorBytes, LocatorArtifact:
		return true
	}
	return false
}

const artifactScheme = "artifact://"

// Locator names the exact sub-range one Evidence row addresses within its
// Source: 1-based inclusive "lines:<start>-<end>", 0-based half-open
// "bytes:<start>-<end>", or the "artifact://<artifact-id>" URI form.
type Locator struct {
	Kind        LocatorKind
	Start       int
	End         int
	ArtifactRef string
}

// String renders l in its canonical text encoding. The zero-value Locator
// (invalid Kind) renders "" -- callers that need a round-trippable string
// must first confirm Kind.Valid().
func (l Locator) String() string {
	switch l.Kind {
	case LocatorLines:
		return fmt.Sprintf("lines:%d-%d", l.Start, l.End)
	case LocatorBytes:
		return fmt.Sprintf("bytes:%d-%d", l.Start, l.End)
	case LocatorArtifact:
		return artifactScheme + l.ArtifactRef
	default:
		return ""
	}
}

// ParseLocator parses an untrusted locator string into a Locator. A
// malformed string, an inverted range, a negative bound, an unknown kind
// prefix, or a non-"artifact://" scheme is a typed invalid-input error.
func ParseLocator(s string) (Locator, error) {
	switch {
	case strings.HasPrefix(s, "lines:"):
		start, end, err := parseRange(s[len("lines:"):])
		if err != nil {
			return Locator{}, err
		}
		return Locator{Kind: LocatorLines, Start: start, End: end}, nil
	case strings.HasPrefix(s, "bytes:"):
		start, end, err := parseRange(s[len("bytes:"):])
		if err != nil {
			return Locator{}, err
		}
		return Locator{Kind: LocatorBytes, Start: start, End: end}, nil
	case strings.HasPrefix(s, artifactScheme):
		ref := s[len(artifactScheme):]
		if ref == "" {
			return Locator{}, cascade.New(cascade.KindInvalidInput, "evidence: empty artifact:// ref")
		}
		return Locator{Kind: LocatorArtifact, ArtifactRef: ref}, nil
	default:
		return Locator{}, cascade.Newf(cascade.KindInvalidInput,
			"evidence: unrecognized locator %q: must be lines:, bytes: or artifact://", s)
	}
}

// MarshalJSON renders l as its canonical text encoding (String), so the
// wire form of a Locator is one string, never a nested object.
func (l Locator) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

// UnmarshalJSON parses l from its canonical text encoding via ParseLocator.
func (l *Locator) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "evidence: decode locator")
	}
	parsed, err := ParseLocator(s)
	if err != nil {
		return err
	}
	*l = parsed
	return nil
}

// parseRange parses "<a>-<b>" into two non-negative, non-inverted bounds.
func parseRange(s string) (start, end int, err error) {
	idx := strings.IndexByte(s, '-')
	if idx <= 0 || idx == len(s)-1 {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed range %q", s)
	}
	start, errStart := strconv.Atoi(s[:idx])
	end, errEnd := strconv.Atoi(s[idx+1:])
	if errStart != nil || errEnd != nil {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed range %q", s)
	}
	if start < 0 || end < 0 {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: negative bound in range %q", s)
	}
	if end < start {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: inverted range %q", s)
	}
	return start, end, nil
}
