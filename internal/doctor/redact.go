package doctor

import (
	"math"
	"regexp"
	"strings"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// Purpose: the RECALL-FIRST bundle redaction detector (§D-31), applied by
//
//	bundle.go to every value and log line written into a diagnostic
//	bundle. A diagnostic bundle is the most likely path for a secret to
//	leave a user's machine (pasted into an issue tracker), so this
//	detector is deliberately biased toward over-redaction: recall over
//	precision, and an unclassifiable value redacts rather than passes
//	through (fail closed).
//
// Inputs: a single value+key pair (RedactValue, for structured fields:
//
//	config, env vars) or a free-text blob (RedactText, for log lines and
//	error/detail strings, which may embed a secret mid-sentence rather
//	than as a whole field value).
//
// Outputs: the input with every matched span replaced by
//
//	"[SECRET-REDACTED]" (a known secret shape) or "[ENTROPY-REDACTED]"
//	(a high-entropy token matching no known shape); a structured field
//	whose key is outside the AllowedFields set is dropped entirely by
//	FilterAllowedFields (see its doc comment for why "dropped" rather
//	than "value replaced" is this ticket's chosen reading of the AC).
//
// Constraints: REUSES internal/runtime's LooksLikeSecret (via
//
//	RedactValue) rather than re-deriving the bearer-prefix/PEM/bare-base64
//	whole-value heuristic C/S-04's config writer and hooks audit trail
//	already ship — RedactValue never bypasses it. It cannot reuse
//	runtime's unexported secretBearerPrefixes list for SUBSTRING
//	scanning (files_scope for this ticket forbids editing
//	config_write_secrets.go to export it), so the substring-level bearer
//	prefixes below are a second, deliberately-duplicated list — see the
//	REUSE note in this ticket's final report for what that costs.
//	§D-31's heuristic set (entropy, known-secret-shape, key/value, DSN/
//	JWT/token, log-payload scrubbing) is implemented in full below; the
//	patterns are intentionally broad (recall-first), documented per-case
//	for what they do and do not catch.
//
// SPORT: placeholder: doctor/redact (ADD).

// Redaction placeholder tokens.
const (
	RedactedSecret  = "[SECRET-REDACTED]"
	RedactedEntropy = "[ENTROPY-REDACTED]"
)

// entropyThreshold is the Shannon-entropy-per-character cutoff (bits/char)
// above which a candidate token is treated as opaque/secret-shaped rather
// than prose. English prose and typical identifiers sit well below 4.0;
// random tokens (base64/hex/uuid-shaped) sit at 4.5-6.0. Recall-first:
// chosen low enough to catch real tokens even at the cost of occasionally
// flagging an unusually random-looking identifier.
const entropyThreshold = 4.0

// minEntropyTokenLen is the minimum candidate length the entropy scan
// considers — shorter tokens do not carry enough samples for the entropy
// estimate to be meaningful, and would produce too many false positives
// on ordinary short words/flags.
const minEntropyTokenLen = 20

// secretNamedKeyPattern matches a field/env-var key naming a credential,
// per §D-31's key/value heuristic list, case-insensitive, substring
// match (a key like "DB_PASSWORD" or "stripe_secret_key" must match, not
// just an exact "password").
var secretNamedKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|apikey|credential|private[_-]?key)`)

// bearerPrefixSubstring matches a known credential-token prefix anywhere
// inside a larger string (log line, file path, error message) — a
// substring-scan counterpart to internal/runtime's whole-value
// LooksLikeSecret prefix check (see doc.go REUSE note for why this list
// is duplicated rather than imported).
var bearerPrefixSubstring = regexp.MustCompile(`\b(sk-|ghp_|gho_|ghs_|github_pat_|xoxb-|xoxp-|xoxa-|ya29\.|AIza|AKIA)[A-Za-z0-9_-]{10,}\b`)

// pemBlockPattern matches a full PEM-armored block (private key, cert,
// etc.), non-greedy across newlines.
var pemBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// dsnPattern matches scheme://user:pass@host connection strings with
// embedded credentials.
var dsnPattern = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@[^\s]+`)

// jwtPattern matches a three-part base64url token (header.payload.sig) —
// the JWT shape.
var jwtPattern = regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)

// longOpaqueRunPattern matches an unbroken >=40-char run of base64-ish
// characters — mirrors runtime's bareBase64Pattern but as a substring
// match rather than an anchored whole-value match.
var longOpaqueRunPattern = regexp.MustCompile(`\b[A-Za-z0-9+/=_-]{40,}\b`)

// entropyCandidatePattern extracts >=minEntropyTokenLen (20) runs of
// token-shaped characters as entropy-scan candidates. The class
// deliberately excludes "[" "]" (and other prose punctuation like quotes
// and parens) so this LAST pass never re-spans a placeholder an earlier
// pass in RedactText already substituted (e.g. "key=[SECRET-REDACTED]"
// must not be re-flagged as one opaque entropy blob and downgraded from
// its specific SECRET tag to the generic ENTROPY one). The literal "20"
// here must match minEntropyTokenLen; redact_test.go's shannon-entropy
// coverage exercises both.
var entropyCandidatePattern = regexp.MustCompile(`[A-Za-z0-9+/=_.:@-]{20,}`)

// shannonEntropy returns s's Shannon entropy in bits per character.
func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	counts := make(map[rune]int)
	for _, r := range s {
		counts[r]++
	}
	n := float64(len(s))
	var entropy float64
	for _, c := range counts {
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// isHighEntropyToken reports whether s is long enough and random-looking
// enough to be treated as an opaque secret-shaped token.
func isHighEntropyToken(s string) bool {
	return len(s) >= minEntropyTokenLen && shannonEntropy(s) >= entropyThreshold
}

// RedactValue redacts a single structured field's value, given its key.
// A secret-named key (secretNamedKeyPattern) is redacted unconditionally,
// regardless of the value's shape (§D-31: "the value side of any key
// matching ... is redacted regardless of value shape"). Otherwise the
// value is run through RedactValue's shape checks in the same order as
// RedactText, then the runtime.LooksLikeSecret whole-value heuristic
// (reuse), then the entropy fallback. An empty value never needs
// redaction (nothing to leak).
func RedactValue(key, value string) string {
	if value == "" {
		return value
	}
	if secretNamedKeyPattern.MatchString(key) {
		return RedactedSecret
	}
	if scanned := RedactText(value); scanned != value {
		return scanned
	}
	if ok, _ := cascaderuntime.LooksLikeSecret(value); ok {
		return RedactedSecret
	}
	if isHighEntropyToken(value) {
		return RedactedEntropy
	}
	return value
}

// RedactText applies the full §D-31 substring-scrub pass to free text
// (a log line, an error message, a file path) — used directly by
// bundle.go for daemon log content and check Detail/Message strings, and
// by RedactValue as the first pass over a single field's value. Order
// matters: key/value pairs and PEM blocks are matched before the
// generic opaque-run/entropy passes so a matched span is replaced with a
// short placeholder before the entropy scan can re-flag pieces of it.
func RedactText(text string) string {
	text = redactKeyValuePairs(text)
	text = pemBlockPattern.ReplaceAllString(text, RedactedSecret)
	text = dsnPattern.ReplaceAllString(text, RedactedSecret)
	text = jwtPattern.ReplaceAllString(text, RedactedSecret)
	text = bearerPrefixSubstring.ReplaceAllString(text, RedactedSecret)
	text = longOpaqueRunPattern.ReplaceAllString(text, RedactedSecret)
	text = redactEntropyCandidates(text)
	return text
}

// redactKeyValuePairs replaces the value half of a "key = value" or
// "key: value" pair where key matches secretNamedKeyPattern, keeping the
// key visible (so a reader can see WHICH field was redacted) per §D-31's
// key/value heuristic.
func redactKeyValuePairs(text string) string {
	pattern := regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|apikey|credential|private[_-]?key)\b(\s*[:=]\s*)(\S+)`)
	return pattern.ReplaceAllString(text, "${1}${2}"+RedactedSecret)
}

// redactEntropyCandidates replaces every remaining >=minEntropyTokenLen
// whitespace-delimited run whose Shannon entropy clears entropyThreshold
// with RedactedEntropy — the last-resort aggressive pass for a
// secret-shaped value matching none of the known patterns above.
func redactEntropyCandidates(text string) string {
	return entropyCandidatePattern.ReplaceAllStringFunc(text, func(tok string) string {
		if isHighEntropyToken(tok) {
			return RedactedEntropy
		}
		return tok
	})
}

// AllowedFields is a field-allowlist for structured bundle sections
// (resolved config, system info): include-known-safe rather than
// exclude-known-bad, per §D-31.
type AllowedFields map[string]struct{}

// NewAllowedFields builds an AllowedFields set from names.
func NewAllowedFields(names ...string) AllowedFields {
	m := make(AllowedFields, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// Allowed reports whether key is in the allowlist.
func (a AllowedFields) Allowed(key string) bool {
	_, ok := a[key]
	return ok
}

// DefaultAllowedFields is the safe field allowlist for the bundle's
// "system info" and "resolved config" sections — keys already known by
// construction to never carry a credential value.
func DefaultAllowedFields() AllowedFields {
	return NewAllowedFields(
		"os", "arch", "go_version", "cascade_version", "num_cpu",
		"schema_version",
		"runtime.profile", "runtime.data_dir",
		"elevation.allow_remote",
		"logging.level", "logging.format",
	)
}

// FilterAllowedFields keeps only the entries of fields whose key is in
// allowed, dropping every other key entirely (§D-31: "only fields in
// AllowedFields pass through into the bundle" — a non-allowlisted key
// does not appear in the output at all, not merely with a redacted
// value). Every kept value additionally passes through RedactValue as
// defense in depth: an allowlisted key can still carry an
// accidentally-secret-shaped value (e.g. a user pastes a token into a
// path field). A nil or empty allowed set drops every field — fail
// closed (Art.1): an empty allowlist must never be read as "allow
// nothing to be filtered," it must redact everything.
func FilterAllowedFields(fields map[string]string, allowed AllowedFields) map[string]string {
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		if !allowed.Allowed(k) {
			continue
		}
		out[k] = RedactValue(k, v)
	}
	return out
}

// RedactLines applies RedactText to each line independently, EXCEPT that
// a PEM block spanning multiple lines needs the whole block joined first
// to match pemBlockPattern's (?s) span — callers with multi-line log
// content should prefer RedactText on the full joined text and
// strings.Split the result, which bundle.go does; RedactLines exists for
// callers that already have a single-line-at-a-time stream (equivalent
// behavior for any secret shape that does not itself span lines, i.e.
// every §D-31 shape except PEM blocks).
func RedactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = RedactText(l)
	}
	return out
}

// redactJoinedLines is the PEM-safe helper bundle.go uses for daemon log
// content: join with "\n", redact as one blob (so pemBlockPattern's
// cross-line match works), then re-split.
func redactJoinedLines(lines []string) []string {
	joined := strings.Join(lines, "\n")
	return strings.Split(RedactText(joined), "\n")
}
