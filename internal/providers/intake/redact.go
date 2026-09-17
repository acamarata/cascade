package intake

// Purpose: keep a credential out of every error this package renders
//   (P1-E16-W4-S35-T11, R-14.262).
// Inputs: an error from the HTTP layer, and the credential the request
//   carried.
// Outputs: the same failure, with the credential replaced by a marker.
// Constraints: the endpoint STRING was already redacted where it is
//   composed; this covers the wrapped CAUSE, which quotes the request URL
//   the transport was handed and is therefore the copy that still had the
//   key in it. One vendor carries the credential in the query string, so
//   any error naming that URL names the key.
// SPORT: internal/providers/intake credential redaction (ADD) -- P1-E16-W4-S35-T11.

import (
	"errors"
	"net/url"
	"strings"
)

// credentialMarker replaces a credential anywhere it would be rendered. It
// matches the spelling the endpoint string already uses, so one failure
// reads consistently across the clause intake composes and the cause it
// wraps.
const credentialMarker = "REDACTED"

// redactCredential returns err with every rendering of key replaced.
//
// By rewriting the TEXT rather than by reaching into the error: the cause
// is whatever the HTTP client returned -- today a *url.Error carrying the
// request URL, tomorrow something else -- and a redaction that depended on
// that type would stop working the first time the transport changed,
// silently and in the direction that leaks.
//
// Both spellings are replaced. A credential travels the query string
// percent-encoded, so an error can quote either form depending on where in
// the stack it was rendered, and replacing only the raw one leaves the
// other behind.
//
// A key shorter than the threshold is NOT used as a search string: a
// one- or two-character value would match inside a host name or a path and
// turn the diagnostic into nonsense. Such a value cannot be a real
// credential, and refusing to redact it keeps the error readable -- the
// callers that matter reject it long before this.
func redactCredential(err error, key string) error {
	if err == nil || len(key) < minRedactableKey {
		return err
	}
	text := err.Error()
	for _, form := range []string{key, url.QueryEscape(key)} {
		if form == "" {
			continue
		}
		text = strings.ReplaceAll(text, form, credentialMarker)
	}
	if text == err.Error() {
		return err
	}
	return errors.New(text)
}

// minRedactableKey is the shortest value this package will treat as a
// credential for redaction. Below it, a substring replace does more damage
// to the message than the value could do to the operator.
const minRedactableKey = 8
