package intake

// Purpose: no error this package renders may contain the credential
//   (P1-E16-W4-S35-T11, R-14.262).
// Constraints: the assertions search the WHOLE rendered error, at every
//   nesting depth, for the credential value. Asserting that the redaction
//   marker is present would pass against a message that carried both --
//   which is exactly what shipped, since the endpoint clause was redacted
//   and the wrapped cause beside it was not.
// SPORT: internal/providers/intake credential redaction (ADD) --
//   P1-E16-W4-S35-T11.

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// theCredential is a value long enough to be treated as one and
// distinctive enough that finding it in a message is unambiguous.
const theCredential = "sk-live-0123456789abcdef"

// transportErrorFor builds the error a real HTTP client returns: one that
// quotes the request URL, credential and all.
func transportErrorFor(rawURL string) error {
	return &url.Error{Op: "Get", URL: rawURL, Err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")}
}

// TestTheShapeProbeFailureNeverQuotesTheCredential is the regression.
//
// The gemini candidate carries the credential in its QUERY STRING, so the
// transport error quotes it. Before this, one sentence showed the same URL
// twice: once as "key=REDACTED" from the endpoint string intake composes,
// and once with the real value from the cause it wrapped.
func TestTheShapeProbeFailureNeverQuotesTheCredential(t *testing.T) {
	doer := &fakeDoer{err: map[string]error{}}
	base := "https://example.invalid"
	for _, target := range probeOrder {
		raw := base + target.pathSuffix
		if target.kind == DriverGemini {
			raw += "?key=" + theCredential
		}
		doer.err[raw] = transportErrorFor(raw)
	}

	_, _, _, err := shapeProbe(context.Background(), doer, testEngine(t), theCredential, base)
	if err == nil {
		t.Fatal("shapeProbe succeeded with every candidate failing")
	}
	assertNoCredential(t, err, "the shape-probe failure")
}

// TestTheMicroVerifyFailureNeverQuotesTheCredential covers the other site
// that hands a transport error to an operator.
func TestTheMicroVerifyFailureNeverQuotesTheCredential(t *testing.T) {
	req := microVerifyRequest(DriverGemini, "https://example.invalid", theCredential, "a-model")
	doer := &fakeDoer{err: map[string]error{req.URL: transportErrorFor(req.URL)}}
	deps, _ := testDeps(t, doer)

	err := microVerify(context.Background(), deps, DriverGemini, "https://example.invalid", theCredential, "a-model")
	if err == nil {
		t.Fatal("microVerify succeeded against a transport that refused the connection")
	}
	assertNoCredential(t, err, "the micro-verify failure")
}

// TestRedactionCoversThePercentEncodedForm: a credential travels the query
// string percent-encoded, and an error rendered further down the stack can
// quote either form. Replacing only the raw one leaves the other behind.
func TestRedactionCoversThePercentEncodedForm(t *testing.T) {
	key := "sk-live-a+b/c=d==" // every character url.QueryEscape rewrites
	raw := "https://example.invalid/v1beta/models?key=" + url.QueryEscape(key)
	got := redactCredential(transportErrorFor(raw), key)
	assertNoCredential(t, got, "the redacted transport error")
}

// TestRedactionLeavesAShortValueAlone: a one- or two-character string
// matches inside a host name, and replacing it would turn the diagnostic
// into nonsense for a value that cannot be a credential anyway.
func TestRedactionLeavesAShortValueAlone(t *testing.T) {
	err := transportErrorFor("https://example.invalid/v1/models")
	if got := redactCredential(err, "e"); got.Error() != err.Error() {
		t.Errorf("a one-character key rewrote the message: %v", got)
	}
	if got := redactCredential(nil, theCredential); got != nil {
		t.Errorf("redactCredential(nil) = %v, want nil", got)
	}
}

// TestRedactionKeepsTheDiagnostic: removing the credential must not remove
// the reason. An error nobody can act on is its own defect.
func TestRedactionKeepsTheDiagnostic(t *testing.T) {
	raw := "https://example.invalid/v1beta/models?key=" + theCredential
	got := redactCredential(transportErrorFor(raw), theCredential).Error()
	for _, want := range []string{"example.invalid", "connection refused", credentialMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("the redacted error lost %q: %s", want, got)
		}
	}
}

// assertNoCredential searches the whole rendered error for the credential.
//
// The whole string, not a prefix and not one clause: the defect this
// guards was a message that redacted one occurrence and printed the next.
func assertNoCredential(t *testing.T, err error, what string) {
	t.Helper()
	rendered := err.Error()
	for _, form := range []string{theCredential, url.QueryEscape(theCredential)} {
		if strings.Contains(rendered, form) {
			t.Errorf("%s contains the credential:\n%s", what, rendered)
		}
	}
	// Unwrapped too, in case a caller renders the cause on its own.
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), theCredential) {
			t.Errorf("%s has a wrapped cause containing the credential:\n%s", what, cause)
		}
	}
}
