// Purpose: locating well-formed tags inside a turn. The rewriter needs
//
//	this to leave alone what a previous pass already tagged, which is
//	what makes a second pass a no-op and a retry safe.
//
// Inputs: a turn's text. Outputs: the byte spans that hold a tag the
//
//	grammar fully accepts.
//
// Constraints: split from rewriter.go under the repo's 300-line file cap
//
//	(Art.10.3), same responsibility boundary as detector_signals.go has
//	beside detector.go. Only a tag ParseTag accepts counts as a tag: a
//	malformed tag-like run has no power to shield a credential from the
//	rewriter, which is the fail-closed reading of an unparseable tag.
//
// SPORT: TURN_REWRITER: ADD (internal/secrets tag scanning).

package secrets

import "bytes"

// tagSpan is one well-formed tag located in a turn.
type tagSpan struct {
	start int
	end   int
}

// scanTags finds every well-formed tag in text.
//
// Only a tag the grammar fully accepts counts. A malformed tag-like run
// is NOT treated as a tag, so it can never shield a credential from being
// rewritten; it stays in the turn as the literal text the user typed,
// which is the right outcome precisely because it is not protecting
// anything. This is the fail-closed reading of an unparseable tag: it
// loses its power to exclude, not the user's words.
func scanTags(text string) []tagSpan {
	var out []tagSpan
	for i := 0; i < len(text); {
		if text[i] != '<' {
			i++
			continue
		}
		span, ok := tagAt(text, i)
		if !ok {
			i++
			continue
		}
		out = append(out, span)
		i = span.end
	}
	return out
}

// tagAt tries to read a tag starting at i, taking the nearest closing
// marker for each candidate type in the grammar's fixed order.
func tagAt(text string, i int) (tagSpan, bool) {
	for _, tagType := range tagTypes {
		if !hasTagOpener(text[i:], tagType) {
			continue
		}
		closing := "</" + string(tagType) + ">"
		at := indexAfter(text, i, closing)
		if at < 0 {
			continue
		}
		end := at + len(closing)
		if _, err := ParseTag([]byte(text[i:end])); err == nil {
			return tagSpan{start: i, end: end}, true
		}
	}
	return tagSpan{}, false
}

// hasTagOpener reports whether rest begins with an opener for tagType.
func hasTagOpener(rest string, tagType TagType) bool {
	opener := "<" + string(tagType)
	if len(rest) <= len(opener) {
		return false
	}
	if rest[:len(opener)] != opener {
		return false
	}
	next := rest[len(opener)]
	return next == '>' || next == ' '
}

// indexAfter returns the index of needle at or after from, or -1.
func indexAfter(text string, from int, needle string) int {
	at := bytes.Index([]byte(text[from:]), []byte(needle))
	if at < 0 {
		return -1
	}
	return from + at
}
