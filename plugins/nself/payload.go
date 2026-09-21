// Purpose (this file): nself_project_info's wire shape and the credential
//
//	scrub every field passes through before it is encoded.
//
// WHY A SCRUB AT ALL, WHEN NOTHING HERE SHOULD CARRY A CREDENTIAL. Two
//
//	independent layers, because one of them is a claim about every future
//	edit rather than about today's code. Layer 1 is construction: the
//	payload is assembled from code-chosen constants, a boolean, a
//	directory path and this plugin's own error text — never from the
//	child's stdout or stderr, which `nself status` fills with service state
//	that can name a database URL. Layer 2 is this file's scrub: any field
//	that nonetheless arrives shaped like a URL with userinfo, or like a
//	secret-named key/value pair, is REPLACED before encoding. The scrub
//	runs regardless of what the egress firewall would have done, because
//	the firewall is a separate subsystem with its own configuration and
//	"the other layer will catch it" is how both layers end up empty.
//
// Inputs: a detection result and a doctor outcome.
// Outputs: a projectInfoResponse whose every string field is scrubbed.
// Constraints: the replacement marker must itself be unmatched by the
//
//	patterns, so scrubbing is idempotent (payload_test.go asserts it).
//
// SPORT: plugins/nself entity (ADD) — P1-E25-W5-S52-T2.

package nself

import (
	"errors"
	"regexp"
)

// probe outcome classifications. Every value here is a CODE-CHOSEN
// constant: not one byte of the child's stdout or stderr reaches a tool
// response.
const (
	probeOutcomeBinaryAbsent = "binary-absent"
	probeOutcomeTimedOut     = "timed-out"
	probeOutcomeRanAndFailed = "ran-and-failed"
	probeOutcomeUnavailable  = "not-runnable"
	probeOutcomeOK           = "ok"
)

// projectInfoResponse is nself_project_info's wire shape.
type projectInfoResponse struct {
	Detected   bool       `json:"detected"`
	Method     string     `json:"method"`
	MarkerPath string     `json:"marker_path,omitempty"`
	Doctor     doctorWire `json:"doctor"`
}

// doctorWire is the doctor leg of that response.
type doctorWire struct {
	BinaryReachable bool   `json:"binary_reachable"`
	BinaryPath      string `json:"binary_path,omitempty"`
	// ProbeOutcome is one of the code-chosen constants above.
	ProbeOutcome string `json:"probe_outcome,omitempty"`
	// ScanWarning names the KIND of ancestor-scan failure the walk saw (a
	// permission denial, typically) without reproducing the path or the
	// operating system's message.
	ScanWarning string `json:"scan_warning,omitempty"`
	// Error is the doctor probe's own typed failure text: this plugin's
	// own words, never a subprocess's.
	Error string `json:"error,omitempty"`
}

// classifyProbe maps a typed probe error onto a code-chosen outcome.
func classifyProbe(err error) string {
	var absent *binaryAbsentError
	var timedOut *probeTimeoutError
	var failed *probeFailedError
	switch {
	case err == nil:
		return probeOutcomeOK
	case errors.As(err, &absent):
		return probeOutcomeBinaryAbsent
	case errors.As(err, &timedOut):
		return probeOutcomeTimedOut
	case errors.As(err, &failed):
		return probeOutcomeRanAndFailed
	default:
		return probeOutcomeUnavailable
	}
}

// scanWarningText is the ONE sentence a scan failure ever contributes.
const scanWarningText = "an ancestor directory could not be inspected; the scan continued past it"

// newProjectInfoResponse assembles the response from a detection result
// and a doctor outcome.
func newProjectInfoResponse(res result, dr doctorResult, derr error) projectInfoResponse {
	out := projectInfoResponse{
		Detected:   res.Detected,
		Method:     res.Method,
		MarkerPath: res.MarkerPath,
		Doctor: doctorWire{
			BinaryReachable: dr.BinaryReachable,
			BinaryPath:      dr.BinaryPath,
			ProbeOutcome:    classifyProbe(res.ProbeErr),
		},
	}
	if res.ScanErr != nil {
		out.Doctor.ScanWarning = scanWarningText
	}
	if derr != nil {
		out.Doctor.Error = derr.Error()
	}
	return out
}

// credentialShapes are the shapes a credential takes in a string this
// payload could ever carry: userinfo in a URL authority, a secret-named
// key/value pair, a bearer token, and a bare hex token.
//
// WHY THE KEY/VALUE PATTERN HAS NO LEADING \b. The confirming review found
// `aws_access_key_id=AKIA…` and `AWS_SECRET_ACCESS_KEY=…` surviving this
// layer: `\b` cannot match between `_` and `access`, because both sides are
// word characters, and it cannot match between `key` and `_id` for the same
// reason. The boundaries here are therefore stated in terms of what a
// SEPARATOR is ([^A-Za-z0-9], which `_` and `-` are) and what a trailing
// key-name suffix looks like ([A-Za-z0-9_-]*), so a secret word embedded in
// a longer environment-variable name still matches.
//
// The bare-hex pattern is the layer of last resort for the one shape the
// egress firewall's own detectors also miss: a token with no name attached.
// 32, 40 and 64 hex digits are the unambiguous lengths (md5/sha1/sha256 and
// every token minted at those widths); shorter runs are left alone, because
// a path segment is routinely 8 or 16 hex digits.
//
// The first pattern deliberately does NOT require a well-formed scheme
// before "://". FuzzNselfProjectInfoPayload found the reason: an input
// whose scheme character is one encoding/json escapes ("&://u:p@h") got
// past a scheme-anchored pattern and then reappeared as "u0026://u:p@h" in
// the encoded bytes. The property worth enforcing is "no userinfo in an
// authority", and that is what this matches — the same property the fuzz
// oracle states independently. Its seed corpus keeps that input.
//
// The authority class excludes only a LITERAL space, not every whitespace
// character, for the same reason. This ticket's live fuzz minimised
// "A://\r@": a raw carriage return is whitespace, so a `\s`-excluding class
// stopped there and skipped the redaction — but encoding/json turns that
// carriage return into the two printable characters `\r`, so userinfo
// reappeared in the encoded bytes. The only raw byte that stays a separator
// after encoding is the space itself. That input is a seed too.
var credentialShapes = []*regexp.Regexp{
	regexp.MustCompile(`://[^/?#\x20]*@`),
	regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(pass|passwd|password|secret|token|api[_\-]?key|access[_\-]?key)[A-Za-z0-9_\-]*\s*[=:]\s*\S`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]{8,}={0,2}`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{32}([0-9a-f]{8}|[0-9a-f]{32})?\b`),
}

// redactedMarker replaces a credential-shaped value. It matches neither
// pattern above, so scrubbing is idempotent.
const redactedMarker = "[redacted credential-shaped value]"

// scrub replaces s entirely when it is credential-shaped. Replacing rather
// than masking part of it is deliberate: a partial mask still leaks the
// host, the user and the database name.
func scrub(s string) string {
	for _, re := range credentialShapes {
		if re.MatchString(s) {
			return redactedMarker
		}
	}
	return s
}

// scrubbed returns r with every string field scrubbed.
func (r projectInfoResponse) scrubbed() projectInfoResponse {
	r.Method = scrub(r.Method)
	r.MarkerPath = scrub(r.MarkerPath)
	r.Doctor.BinaryPath = scrub(r.Doctor.BinaryPath)
	r.Doctor.ProbeOutcome = scrub(r.Doctor.ProbeOutcome)
	r.Doctor.ScanWarning = scrub(r.Doctor.ScanWarning)
	r.Doctor.Error = scrub(r.Doctor.Error)
	return r
}
