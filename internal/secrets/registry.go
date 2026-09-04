// Purpose: the detector's pattern registry - the named credential shapes
//
//	Detector.Scan matches, each with the confidence a match on its own
//	earns. Split from detector.go so the question "what does cascade
//	consider a credential" has one file to read and one file to review.
//
// Inputs: none at construction; DefaultRegistry is a pure value.
// Outputs: an ordered []Pattern. Order is fixed and part of the contract:
//
//	Art.7 determinism means two runs over the same content must produce
//	the same hits in the same order, so nothing here may iterate a map.
//
// Constraints: precision-first. Every pattern in this table is anchored
//
//	on a structural marker that ordinary content does not carry by
//	accident - a vendor key prefix, the base64 of a JSON header, a PEM
//	armour line, credentials embedded in a URL authority. Shape alone
//	("this looks random") is NOT in this table; it is the entropy signal
//	in detector.go, which never reaches the quarantine threshold without
//	a corroborating name. This file makes no network call and reads no
//	file, by construction, and nothing here ever records a matched value.
//
// SPORT: SECRETS_DETECTOR: ADD (internal/secrets.Registry, Confidence).

package secrets

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// Class names one kind of credential material. It is metadata about a
// span, never the span's contents: a Class travels into logs, quarantine
// records and doctor output where a value must never go.
type Class string

// The credential classes the default registry recognises. ClassHighEntropy
// is deliberately last and deliberately different: it is the shape-only
// signal, and on its own it never reaches the quarantine threshold.
const (
	// ClassAPIKey is a vendor-prefixed API key (sk-, ghp_, AKIA, ...).
	ClassAPIKey Class = "api-key"
	// ClassJWT is a JSON Web Token: a base64url header that decodes to a
	// JSON object, then a payload and a signature.
	ClassJWT Class = "jwt"
	// ClassBearer is a bearer token in an Authorization header or an
	// equivalent "Bearer <token>" form.
	ClassBearer Class = "bearer"
	// ClassPEM is a PEM-armoured PRIVATE KEY block. Certificates and
	// public keys are deliberately excluded: they are not secret.
	ClassPEM Class = "pem-private-key"
	// ClassConnString is a connection string carrying an inline password
	// in its URL authority (scheme://user:pass@host).
	ClassConnString Class = "connection-string"
	// ClassBase64JSON is a base64 blob that decodes to a JSON object with
	// a credential-named field.
	ClassBase64JSON Class = "base64-json"
	// ClassHighEntropy is an opaque high-entropy run matching no known
	// shape. Alone it is a hint, not a finding: see Detector.Scan.
	ClassHighEntropy Class = "high-entropy"
)

// Confidence is how sure the detector is that a span is credential
// material, in [0,1]. It is compared against DetectionConfig's
// ConfidenceThreshold; only a hit that reaches the threshold is eligible
// for quarantine.
type Confidence float64

// The confidence rungs the registry and the scorer use. They are named
// rather than sprinkled as literals because the gap between Corroborated
// and Weak is the whole precision-first design: the default threshold
// (0.8) sits inside that gap on purpose.
const (
	// ConfidenceWeak is a shape-only signal: high entropy, no structural
	// marker and no credential-named field nearby. Below every sane
	// threshold, and that is the point.
	ConfidenceWeak Confidence = 0.40
	// ConfidenceStructured is a shape that is ALSO a common non-secret
	// identifier (a UUID, a git object id). Capped below the threshold
	// even when a credential-named field sits beside it: see
	// detector.go's structuredIdentifier for why, and what it costs.
	ConfidenceStructured Confidence = 0.60
	// ConfidenceCorroborated is a high-entropy run with a
	// credential-named field within nameWindowBytes. This is the rung a
	// context-name match lifts an otherwise-shapeless span to.
	ConfidenceCorroborated Confidence = 0.90
	// ConfidenceCertain is a match on an unambiguous structural marker -
	// a vendor key prefix, a JWT, a PEM private-key header.
	ConfidenceCertain Confidence = 0.95
	// ConfidenceProven is reserved for a shape that cannot occur by
	// accident at all: PEM private-key armour.
	ConfidenceProven Confidence = 1.0
)

// Pattern is one named credential shape.
type Pattern struct {
	// Class is the credential class a match belongs to.
	Class Class
	// Name identifies the pattern for diagnostics. It never contains a
	// value and is safe to log.
	Name string
	// Expr matches the credential span. It must match the CREDENTIAL,
	// not the surrounding line, because DetectionHit.Offset/Len are
	// reported from this match and a rewriter replaces exactly that span.
	Expr *regexp.Regexp
	// Weight is the confidence a match earns with no corroboration.
	Weight Confidence
	// Decode, when non-nil, is a second gate a regex match must also
	// pass: the regex finds candidates cheaply, Decode confirms them.
	// This is what keeps ClassBase64JSON from firing on every base64
	// blob in the tree.
	Decode func(match string) bool
}

// Registry is the ordered pattern table Detector.Scan walks. The zero
// value matches nothing; use DefaultRegistry.
type Registry struct {
	patterns []Pattern
}

// Patterns returns the registry's patterns in their fixed order. The
// returned slice is a copy, so a caller cannot reorder the table and
// silently change which of two overlapping matches wins.
func (r Registry) Patterns() []Pattern {
	return append([]Pattern(nil), r.patterns...)
}

// apiKeyExpr matches a vendor-prefixed API key. Each prefix here is a
// registered, vendor-published marker; none of them occurs in ordinary
// prose, which is what makes a match certain on its own. The trailing
// length bound keeps a bare mention of the prefix ("keys start with sk-")
// from matching.
var apiKeyExpr = regexp.MustCompile(
	`(sk-[A-Za-z0-9_-]{20,}|` +
		`gh[pousr]_[A-Za-z0-9]{20,}|` +
		`github_pat_[A-Za-z0-9_]{20,}|` +
		`xox[baprs]-[A-Za-z0-9-]{10,}|` +
		`ya29\.[A-Za-z0-9._-]{20,}|` +
		`AIza[A-Za-z0-9_-]{30,}|` +
		`AKIA[A-Z0-9]{16}|` +
		`glpat-[A-Za-z0-9_-]{16,}|` +
		`npm_[A-Za-z0-9]{30,}|` +
		`shpat_[A-Za-z0-9]{28,})`)

// jwtExpr matches a three-part base64url token whose FIRST part starts
// with "eyJ" - the base64 of `{"`, i.e. a JSON object header. Requiring
// the header shape is what separates a JWT from the near-miss class of
// "any dotted base64-ish run", which ordinary content produces often.
var jwtExpr = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)

// bearerExpr matches a bearer token together with the keyword that
// declares it. The keyword is inside the match on purpose: "Bearer" is
// the structural marker, and without it the token is just an opaque run.
var bearerExpr = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`)

// pemBlockExpr and pemHeaderExpr match PEM PRIVATE KEY armour only. A
// CERTIFICATE or PUBLIC KEY block is armoured identically and is not
// secret; matching those would be the archetypal false positive that
// teaches an operator to switch the detector off.
//
// The whole block is matched in preference to its header so that ONE hit
// covers the key body: a header-only hit would leave the base64 body to
// be picked up separately by the entropy signal, reporting one key twice.
// The header-only pattern remains as the fallback for a truncated block
// (a key pasted without its END line is still a key).
var (
	pemBlockExpr = regexp.MustCompile(
		`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----[\s\S]*?-----END (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
	pemHeaderExpr = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
)

// connStringExpr matches credentials embedded in a URL authority. The
// password run is bounded at 4 characters so "http://user:@host" and
// similar empty-credential forms do not match.
var connStringExpr = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:[^\s/@]{4,}@[^\s]+`)

// base64RunExpr finds base64 candidates for the ClassBase64JSON decode
// gate. The regex alone is meaningless - decodesToCredentialJSON is the
// real test.
var base64RunExpr = regexp.MustCompile(`\b[A-Za-z0-9+/_-]{24,}={0,2}`)

// credentialFieldExpr is the credential-name vocabulary, used both by the
// base64-JSON decode gate (does the decoded object name a credential?)
// and by detector.go's context-name heuristic.
var credentialFieldExpr = regexp.MustCompile(`(?i)key|secret|token|pass|cred|auth`)

// decodesToCredentialJSON reports whether match decodes to a JSON object
// naming a credential field. It tries the two base64 alphabets and both
// padding conventions, because a blob copied out of a config file may be
// any of the four, and returns false for anything it cannot decode - an
// undecodable blob is not evidence of a credential.
func decodesToCredentialJSON(match string) bool {
	trimmed := strings.TrimRight(match, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		raw, err := enc.DecodeString(trimmed)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(text, "{") || !strings.Contains(text, ":") {
			continue
		}
		if credentialFieldExpr.MatchString(text) {
			return true
		}
	}
	return false
}

// defaultPatterns is the ordered table. Order matters where two patterns
// can match the same span: the FIRST match at a given offset wins, so the
// most specific shapes are listed first. A JWT is also a base64 run; a
// connection string also contains an opaque password.
var defaultPatterns = []Pattern{
	{Class: ClassPEM, Name: "pem-private-key-block", Expr: pemBlockExpr, Weight: ConfidenceProven},
	{Class: ClassPEM, Name: "pem-private-key-header", Expr: pemHeaderExpr, Weight: ConfidenceProven},
	{Class: ClassAPIKey, Name: "vendor-api-key-prefix", Expr: apiKeyExpr, Weight: ConfidenceCertain},
	{Class: ClassJWT, Name: "jwt-triplet", Expr: jwtExpr, Weight: ConfidenceCertain},
	{Class: ClassConnString, Name: "url-authority-password", Expr: connStringExpr, Weight: ConfidenceCorroborated},
	{Class: ClassBearer, Name: "bearer-token", Expr: bearerExpr, Weight: ConfidenceCorroborated},
	{
		Class:  ClassBase64JSON,
		Name:   "base64-json-credential",
		Expr:   base64RunExpr,
		Weight: ConfidenceCorroborated,
		Decode: decodesToCredentialJSON,
	},
}

// DefaultRegistry returns the built-in pattern table: six named
// credential classes, in the fixed order Detector.Scan walks them.
func DefaultRegistry() Registry {
	return Registry{patterns: defaultPatterns}
}

// classDefaultNames is the fallback SuggestedName per class, used when a
// hit carries no usable context name (a PEM block in a file of its own
// has no field name anywhere near it). Every entry is a valid secret name
// under validateSecretName, which registry_test.go asserts.
var classDefaultNames = map[Class]string{
	ClassAPIKey:      "API_KEY",
	ClassJWT:         "JWT_TOKEN",
	ClassBearer:      "BEARER_TOKEN",
	ClassPEM:         "PRIVATE_KEY",
	ClassConnString:  "CONNECTION_STRING",
	ClassBase64JSON:  "ENCODED_CREDENTIAL",
	ClassHighEntropy: "OPAQUE_SECRET",
}

// defaultNameFor returns the fallback name for class. An unknown class
// falls back to the generic name rather than to the empty string: a hit
// with no name at all cannot be promoted into the vault, which would make
// the finding unactionable.
func defaultNameFor(class Class) string {
	if name, ok := classDefaultNames[class]; ok {
		return name
	}
	return "OPAQUE_SECRET"
}
