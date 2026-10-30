// Purpose: locating the tag spans the rehydrate direction must resolve.
//
//	It is a second decoder over the same grammar as tagscan.go and it is
//	deliberately NOT the same function: the rewrite direction treats a
//	malformed tag-like run as ordinary prose, because a broken tag there
//	protects nothing and the user's words are worth keeping. On this
//	direction the same bytes are a refusal, because a run that opens like
//	a tag and does not parse is either a tag this code failed to resolve
//	or an attempt to smuggle one past the parser.
//
// Inputs: content bytes. Outputs: the spans and their parsed tags, or
//
//	ErrMalformedTag naming the offset.
//
// Constraints: no allocation on the no-tag path beyond the empty result;
//
//	total over arbitrary bytes (FuzzRehydrateScan).
//
// SPORT: REHYDRATE_CHANNEL: ADD (internal/secrets tag-span scanning).
//
//	REHYDRATION_TAG_NAMES: ADD (RehydrationTagNames over the same spans).

package secrets

import (
	"bytes"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rehydrationSpan is one tag located in content, with the tag it parsed
// to. The tag is carried rather than re-parsed later so the bytes are
// decoded exactly once.
type rehydrationSpan struct {
	start int
	end   int
	tag   Tag
}

// RehydrationTagNames returns the tags RehydrateOnly would resolve in
// content, in order: exactly the spans its own scan locates, so a caller
// deriving a permitted set from content (a config's declared bindings)
// sees the same tags the guard is later asked about. Content that is not
// valid UTF-8 or holds a tag-like run the grammar refuses is
// ErrMalformedTag, exactly as RehydrateOnly refuses it.
func RehydrationTagNames(content []byte) ([]Tag, error) {
	spans, err := rehydrationSpans(content)
	if err != nil {
		return nil, err
	}
	tags := make([]Tag, len(spans))
	for i, span := range spans {
		tags[i] = span.tag
	}
	return tags, nil
}

// rehydrationSpans is the one entry to the scan both RehydrateOnly and
// RehydrationTagNames use: the UTF-8 check, then the span walk.
func rehydrationSpans(content []byte) ([]rehydrationSpan, error) {
	if !utf8.Valid(content) {
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrMalformedTag,
			"secrets: content must be valid UTF-8 before it can be rehydrated")
	}
	return scanRehydrationSpans(content)
}

// scanRehydrationSpans finds every typed tag in content, refusing any run
// that opens like one and does not satisfy the grammar.
func scanRehydrationSpans(content []byte) ([]rehydrationSpan, error) {
	var out []rehydrationSpan
	for i := 0; i < len(content); {
		if content[i] != '<' {
			i++
			continue
		}
		tagType, ok := openerAt(content, i)
		if !ok {
			i++
			continue
		}
		span, err := spanFor(content, i, tagType)
		if err != nil {
			return nil, err
		}
		out = append(out, span)
		i = span.end
	}
	return out, nil
}

// openerAt reports which type's opener begins at i, if any. At most one
// can match: the five opener strings are distinct and none is a prefix of
// another once its delimiter is included.
func openerAt(content []byte, i int) (TagType, bool) {
	rest := string(content[i:])
	for _, tagType := range tagTypes {
		if hasTagOpener(rest, tagType) {
			return tagType, true
		}
	}
	return "", false
}

// spanFor reads the tag of tagType that opens at i. A missing closing
// marker and a body the grammar refuses are both ErrMalformedTag: this
// direction never falls back to treating the run as prose.
func spanFor(content []byte, i int, tagType TagType) (rehydrationSpan, error) {
	closing := []byte("</" + string(tagType) + ">")
	at := bytes.Index(content[i:], closing)
	if at < 0 {
		return rehydrationSpan{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedTag,
			"secrets: a <%s> tag at offset %d is never closed", string(tagType), i)
	}
	end := i + at + len(closing)
	tag, err := ParseTag(content[i:end])
	if err != nil {
		return rehydrationSpan{}, cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedTag,
			"secrets: a tag-like run at offset %d does not parse: %v", i, err)
	}
	return rehydrationSpan{start: i, end: end, tag: tag}, nil
}
