package learn

// Purpose: the credential gate every telemetry writer input passes before
//   any decoder or shape rule (R-21.152's credential canary). It matches the
//   tree's own registry (internal/secrets) instead of keeping a second,
//   narrower list of credential shapes here, so a class the registry learns
//   is refused by the writers the same day.
// Inputs: one string value at a time.
// Outputs: true when any default-registry pattern matches a span of the
//   value and that span, or for a Decode pattern any window of it, passes
//   the pattern's Decode step.
// Constraints: patterns only, each matched on its own. The detector's
//   shape-only entropy signal plays no part, so it can never mask a pattern
//   hit by winning an overlap, and an opaque hex id, UUID or digest id
//   (which no pattern decodes as a credential) still passes. An empty
//   pattern table, or a pattern with no expression, fails closed: every
//   non-empty value counts as a hit. A Decode pattern's run is tried at
//   every start offset, decoding the longest same-alphabet run from there
//   in each base64 alphabet (P1-BF-R94), because the run class also
//   swallows adjacent id characters ("job-" + blob, blob + "-+x") and a
//   misaligned or mixed-alphabet window never decodes. No end trim: the
//   decoder reads the head, so trailing characters are harmless. That scan
//   is bounded: a run longer than maxDecodeRun, or a value needing more
//   than maxDecodeCalls Decode calls, fails closed instead of being
//   scanned. Nothing here records or returns the value.
// SPORT: internal.learn.credentialShaped/ADDED (P1-CAP-02).

import (
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/secrets"
)

// credentialPatterns reads the default registry's pattern table once per
// process.
var credentialPatterns = sync.OnceValue(func() []secrets.Pattern {
	return secrets.DefaultRegistry().Patterns()
})

// credentialShaped reports whether raw carries a span that a default
// registry pattern matches and confirms. Entropy-only signals do not count.
func credentialShaped(raw string) bool {
	if raw == "" {
		return false
	}
	patterns := credentialPatterns()
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if patternMatches(p, raw) {
			return true
		}
	}
	return false
}

// Bounds on the Decode-pattern run scan. A run of n characters costs at
// most two Decode calls per start offset (one per base64 alphabet), each
// linear in its window, so O(n) calls and O(n^2) decoded characters; the
// caps keep that at most maxDecodeCalls calls of at most maxDecodeRun+2
// characters per pattern per value, and both fail closed.
const (
	// minDecodeWindow is the shortest window handed to Decode, the run
	// length base64RunExpr itself requires.
	minDecodeWindow = 24
	// maxDecodeRun is the longest alphabet run scanned offset by offset;
	// a longer run counts as a hit.
	maxDecodeRun = 1024
	// maxDecodeCalls caps Decode calls for one pattern over one value;
	// running out counts as a hit.
	maxDecodeCalls = 16384
)

// patternMatches reports whether p's expression matches a span of raw that
// also passes p's Decode step (when p has one) at some window of the span.
// A pattern with no expression fails closed.
func patternMatches(p secrets.Pattern, raw string) bool {
	if p.Expr == nil {
		return true
	}
	budget := maxDecodeCalls
	for _, loc := range p.Expr.FindAllStringIndex(raw, -1) {
		if p.Decode == nil || decodesAnyRun(p.Decode, raw[loc[0]:loc[1]], &budget) {
			return true
		}
	}
	return false
}

// decodesAnyRun reports whether decode accepts, for some start offset i of
// span and either base64 alphabet (standard "+/" or URL "-_"), the longest
// run of that alphabet starting at i, cut by one character when its length
// is 1 mod 4 and carrying the span's "=" padding when it reaches it. Only
// windows of at least minDecodeWindow characters are tried (P1-BF-R94).
// The registry's decoder reads the decoded head only, so characters after
// a credential inside the same run cannot hide it, and a character of the
// other alphabet ends the run instead of spoiling the window. A run over
// maxDecodeRun characters, or a scan that would exceed the remaining
// budget of Decode calls, fails closed (true).
func decodesAnyRun(decode func(string) bool, span string, budget *int) bool {
	body := strings.TrimRight(span, "=")
	if len(body) > maxDecodeRun {
		return true
	}
	stdEnd, urlEnd := 0, 0
	for i := 0; i+minDecodeWindow <= len(body); i++ {
		stdEnd = alphabetRunEnd(body, i, stdEnd, inStdAlphabet)
		urlEnd = alphabetRunEnd(body, i, urlEnd, inURLAlphabet)
		for k, end := range [2]int{stdEnd, urlEnd} {
			if k == 1 && end == stdEnd {
				continue // same window as the standard alphabet's
			}
			w := runWindow(span, len(body), i, end)
			if len(w) < minDecodeWindow {
				continue
			}
			if *budget <= 0 {
				return true
			}
			*budget--
			if decode(w) {
				return true
			}
		}
	}
	return false
}

// alphabetRunEnd returns the end of the longest run of in-alphabet bytes
// of body starting at i. prev is the end returned for an earlier offset:
// when it lies past i, that run already covers i and ends there, which
// keeps a whole scan linear.
func alphabetRunEnd(body string, i, prev int, in func(byte) bool) int {
	if prev > i {
		return prev
	}
	end := i
	for end < len(body) && in(body[end]) {
		end++
	}
	return end
}

// runWindow returns span[i:end] cut so its length is not 1 mod 4 (no
// base64 decoder accepts that length), with span's trailing "=" padding
// appended when the run reaches the padding uncut. bodyLen is the length
// of span without that padding.
func runWindow(span string, bodyLen, i, end int) string {
	if (end-i)%4 == 1 {
		return span[i : end-1]
	}
	if end == bodyLen {
		return span[i:]
	}
	return span[i:end]
}

// inStdAlphabet and inURLAlphabet report base64 alphabet membership.
func inStdAlphabet(c byte) bool { return isAlnumByte(c) || c == '+' || c == '/' }
func inURLAlphabet(c byte) bool { return isAlnumByte(c) || c == '-' || c == '_' }

// isAlnumByte reports whether c is an ASCII letter or digit.
func isAlnumByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
