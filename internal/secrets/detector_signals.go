// Purpose: the detector's two non-pattern signals - Shannon entropy over
//
//	value-shaped runs, and the context-name heuristic that corroborates
//	them - plus the shape exclusions that keep ordinary identifiers out
//	of the findings. Split from detector.go under the repo's 300-line
//	file cap (Art.10.3); same responsibility boundary, one file per
//	question: detector.go decides WHAT is reported, this file decides
//	how confident a shapeless run is allowed to be.
//
// Inputs: the scanned text and a candidate offset.
// Outputs: booleans, scores and an UPPER_SNAKE name suggestion. Nothing
//
//	here returns, stores or logs the candidate bytes themselves.
//
// Constraints: pure functions, no I/O, no clock, no randomness. Every
//
//	exclusion below is a deliberate, documented false negative; see each
//	one's comment for what it costs.
//
// SPORT: SECRETS_DETECTOR: ADD (internal/secrets.Detector signals).

package secrets

import (
	"math"
	"regexp"
	"strings"
)

// tokenRunExpr extracts value-shaped runs. The alphabet is the one a
// credential is written in; "=" appears only as trailing base64 padding
// and never inside the run, so a run can never swallow the `NAME=` on its
// left - which matters, because that name is exactly what contextName
// needs to find there to corroborate the run.
var tokenRunExpr = regexp.MustCompile(`[A-Za-z0-9+/_.-]{` +
	itoa(minEntropyRun) + `,}={0,2}`)

// tokenRuns returns the [start,end) offsets of every candidate run.
func tokenRuns(text string) [][]int {
	return tokenRunExpr.FindAllStringIndex(text, -1)
}

// opaqueCandidate reports whether span looks like an opaque token rather
// than an identifier or a sentence fragment. The discriminator is a digit
// beside a letter: real opaque credentials essentially always mix them,
// while `getUserByIdentifier` and `AWS_SECRET_ACCESS_KEY` do not.
//
// The cost is named rather than hidden: a purely alphabetic secret with
// no vendor prefix is invisible to the ENTROPY signal (a pattern match
// still finds it). That is the deliberate direction of the trade - see
// docs/security-posture.md §Quarantine.
func opaqueCandidate(span string) bool {
	var hasDigit, hasLetter bool
	for _, r := range span {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

// uuidExpr and hexIDExpr are the two shapes that dominate the false
// positives of every entropy-based detector: a UUID and a git object id
// both sit above any usable entropy floor and both appear constantly in
// ordinary developer content.
var (
	uuidExpr  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexIDExpr = regexp.MustCompile(`^(?:[0-9a-f]{32,}|[0-9A-F]{32,})$`)
	digitExpr = regexp.MustCompile(`^[0-9]+$`)
)

// structuredIdentifier reports whether span is a well-known non-secret
// identifier shape. Such a span is capped at ConfidenceStructured, below
// any sane threshold, EVEN when a credential-named field sits beside it
// (`request_token_id = <uuid>` is a request id, not a token).
//
// This is a deliberate false NEGATIVE: a credential that happens to be
// UUID-shaped or a 40-character lowercase hex string is not quarantined
// on the entropy signal alone. It is accepted because the alternative -
// quarantining every trace id, commit sha and checksum in a developer's
// notes - is the behaviour that gets a detector switched off, which
// leaks everything rather than one thing.
func structuredIdentifier(span string) bool {
	return uuidExpr.MatchString(span) || hexIDExpr.MatchString(span) || digitExpr.MatchString(span)
}

// shannonEntropy returns span's Shannon entropy in bits per character.
// Byte-wise by design: credential material is ASCII, and counting runes
// would let a multi-byte blob inflate its own score.
func shannonEntropy(span string) float64 {
	if span == "" {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(span); i++ {
		counts[span[i]]++
	}
	n := float64(len(span))
	var entropy float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// itoa is a tiny local decimal formatter used to build tokenRunExpr from
// minEntropyRun, so the constant and the regex can never drift apart.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// contextName looks back up to nameWindowBytes for a credential-named
// field and, when it finds one, returns the UPPER_SNAKE name derived from
// it. The boolean is the corroboration signal; the string is the naming
// output, and a caller may get true with an unusable string (a keyword
// surrounded by punctuation), which suggestedName then falls back on.
func contextName(text string, offset int) (bool, string) {
	start := offset - nameWindowBytes
	if start < 0 {
		start = 0
	}
	window := text[start:offset]
	locs := credentialFieldExpr.FindAllStringIndex(window, -1)
	if len(locs) == 0 {
		return false, ""
	}
	last := locs[len(locs)-1]
	return true, upperSnake(nameWords(window[:wordEnd(window, last[1])]))
}

// wordEnd extends end rightwards to the end of the word the keyword sits
// inside, so "Authorization:" yields AUTHORIZATION rather than the bare
// AUTH the keyword alone would give.
func wordEnd(window string, end int) int {
	for end < len(window) && isNameByte(window[end]) {
		end++
	}
	return end
}

// isNameByte reports whether b can appear inside a field name.
func isNameByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// nameWords collects the words that make up a suggested name, ending at
// the credential keyword.
//
// A compound identifier (OPENAI_API_KEY) is self-contained and is used on
// its own. A bare keyword ("password") is not a usable name by itself, so
// up to two preceding words are folded in - which is what turns the
// plan's worked example, topic "wifi" plus the phrase "my password", into
// WIFI_PASSWORD.
func nameWords(prefix string) []string {
	fields := strings.FieldsFunc(prefix, func(r rune) bool { return !isNameByte(byte(r)) || r > 127 })
	if len(fields) == 0 {
		return nil
	}
	words := subWords(fields[len(fields)-1])
	if len(words) < 2 {
		words = nil
		start := len(fields) - 3
		if start < 0 {
			start = 0
		}
		for _, field := range fields[start:] {
			words = append(words, subWords(field)...)
		}
	}
	if len(words) > maxSuggestedNameWords {
		words = words[len(words)-maxSuggestedNameWords:]
	}
	return words
}

// subWords splits one field on the separators a compound identifier uses.
func subWords(field string) []string {
	return strings.FieldsFunc(field, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
}

// nameStopWords are possessives and articles that carry no meaning in a
// vault name: "my wifi password" is WIFI_PASSWORD, not MY_WIFI_PASSWORD.
var nameStopWords = map[string]bool{"my": true, "the": true, "a": true, "an": true, "our": true, "your": true}

// upperSnake renders words as an UPPER_SNAKE name, dropping stop words.
func upperSnake(words []string) string {
	var kept []string
	for _, w := range words {
		lower := strings.ToLower(w)
		if lower == "" || nameStopWords[lower] {
			continue
		}
		kept = append(kept, strings.ToUpper(w))
	}
	return strings.Join(kept, "_")
}

// suggestedName returns name when it is a usable vault name, and the
// class default otherwise. A hit's SuggestedName is never empty: it is
// what `cascade vault set --from-quarantine` stores the value under, so
// an empty one would make the finding unpromotable.
func suggestedName(name string, class Class) string {
	if name != "" && !digitExpr.MatchString(name[:1]) && validateSecretName(name) == nil {
		return name
	}
	return defaultNameFor(class)
}
