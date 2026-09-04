// Purpose: the OAuth broker's error vocabulary, and the redacted secret
//
//	type every token, authorization code and PKCE verifier is carried in.
//
// Inputs: nothing external. Every constructor here takes only metadata -
//
//	a provider id, an account label, a state length. No constructor in
//	this file accepts a token, a code, or a verifier, which is what makes
//	"a token cannot reach an error message" a property of the type system
//	rather than a rule a reviewer has to check.
//
// Outputs: exported sentinels callers match with errors.Is, and unexported
//
//	constructors that wrap them with a pkg/cascade Kind.
//
// Constraints: mirrors internal/secrets' existing custody errors - fail
//
//	closed, name the record, never the value. oauthSecret implements
//	fmt.Formatter, fmt.Stringer, fmt.GoStringer and json.Marshaler so that
//	EVERY formatting verb (%s %v %q %x %#v), every Print, and every JSON
//	encode of one yields the redaction placeholder. A caller who wants the
//	bytes must call Bytes() and say so.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets.OAuthBroker errors).

package secrets

import (
	"errors"
	"fmt"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// redactedPlaceholder is what an oauthSecret renders as, everywhere. It is
// a fixed string with no length, prefix or suffix of the real value: a
// "sk-...abcd" style preview is a dictionary attack surface on a short
// secret, the same reasoning that made the quarantine ledger's digest keyed
// rather than a bare hash.
const redactedPlaceholder = "[redacted]"

// oauthSecret carries credential material - an access token, a refresh
// token, an authorization code, or a PKCE verifier - in a form that cannot
// be printed. The byte slice is unexported and every rendering path is
// overridden, so the only way to reach the value is Bytes(), which reads as
// an explicit decision at the call site.
type oauthSecret struct {
	b []byte
}

// newOAuthSecret takes ownership of b. Callers must not retain b.
func newOAuthSecret(b []byte) oauthSecret { return oauthSecret{b: b} }

// Bytes returns the underlying value. The single explicit disclosure path.
func (s oauthSecret) Bytes() []byte { return s.b }

// Len reports the value's length. Safe metadata: it distinguishes "absent"
// from "present" without disclosing anything about the value's content.
func (s oauthSecret) Len() int { return len(s.b) }

// Empty reports whether no value is held.
func (s oauthSecret) Empty() bool { return len(s.b) == 0 }

// Zero overwrites the value in place and drops the reference.
func (s *oauthSecret) Zero() {
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

// String implements fmt.Stringer.
func (s oauthSecret) String() string { return redactedPlaceholder }

// GoString implements fmt.GoStringer, so %#v redacts too.
func (s oauthSecret) GoString() string { return redactedPlaceholder }

// Format implements fmt.Formatter, which takes precedence over Stringer for
// every verb - including %x, %d and %q, which Stringer alone does not cover.
func (s oauthSecret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redactedPlaceholder)
}

// MarshalJSON implements json.Marshaler, so an oauthSecret embedded in a
// struct that is serialized into a diagnostic encodes as the placeholder
// rather than as its value.
func (s oauthSecret) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redactedPlaceholder + `"`), nil
}

// The OAuth broker's sentinels. Each is matched with errors.Is; each is
// wrapped by exactly one constructor below, which attaches the taxonomy
// Kind. Wrapping (rather than exporting *cascade.Error values directly)
// keeps the Kind authoritative for control flow while letting a caller test
// for the specific condition.
var (
	// ErrNoInput reports that CASCADE_NO_INPUT=1 forbids the interactive
	// browser flow this operation requires.
	ErrNoInput = errors.New("secrets: interactive OAuth authorization is disabled by CASCADE_NO_INPUT")
	// ErrStateMismatch reports a callback whose state parameter was
	// unknown or already consumed. This is the CSRF refusal: accepting an
	// unsolicited callback is how an attacker binds their own account into
	// the operator's session.
	ErrStateMismatch = errors.New("secrets: OAuth callback state is unknown or already used")
	// ErrStateExpired reports a callback for an authorization that has
	// passed its deadline.
	ErrStateExpired = errors.New("secrets: OAuth callback state has expired")
	// ErrPKCEMismatch reports that the authorization server rejected the
	// code verifier.
	ErrPKCEMismatch = errors.New("secrets: the authorization server rejected the PKCE code verifier")
	// ErrCallbackTimeout reports that no callback arrived before the
	// authorization deadline.
	ErrCallbackTimeout = errors.New("secrets: no OAuth callback arrived before the deadline")
	// ErrMalformedCallback reports a redirect query that could not be
	// parsed, or that carried a duplicated or oversized parameter.
	ErrMalformedCallback = errors.New("secrets: the OAuth callback query is malformed")
	// ErrMalformedTokenResponse reports a token-endpoint body that was not
	// a usable token response.
	ErrMalformedTokenResponse = errors.New("secrets: the OAuth token response is malformed")
	// ErrAuthorizationDenied reports that the authorization server returned
	// an error= response instead of a code.
	ErrAuthorizationDenied = errors.New("secrets: the authorization server refused the authorization request")
	// ErrTokenExpired reports that the stored access token is past its
	// expiry and no refresh token is available to replace it.
	ErrTokenExpired = errors.New("secrets: the stored OAuth access token has expired and cannot be refreshed")
	// ErrGrantRevoked reports that the refresh token is no longer accepted:
	// the grant was revoked underneath cascade. The stored credential is
	// purged when this is raised, so no later call serves the dead token.
	ErrGrantRevoked = errors.New("secrets: the OAuth grant was revoked by the provider")
	// ErrInvalidGrant reports the RFC 6749 §5.2 "invalid_grant" refusal.
	// It is deliberately NOT classified further at the decoder: RFC 7636
	// §4.6 gives the same code to a PKCE verifier mismatch (during Start)
	// and to a revoked or expired refresh token (during Refresh), and only
	// the caller knows which exchange it ran.
	ErrInvalidGrant = errors.New("secrets: the authorization server returned invalid_grant")
	// ErrCredentialInconsistent reports that a refresh could not be
	// committed and the stored credential's consistency could not be
	// proven. It is deliberately loud: a half-written credential must never
	// be presented as a working one.
	ErrCredentialInconsistent = errors.New("secrets: the stored OAuth credential could not be updated consistently")
)

func errOAuthNoInput(providerID string) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrNoInput,
		"secrets: %s needs an interactive browser authorization, which CASCADE_NO_INPUT=1 forbids", providerID)
}

func errOAuthStateMismatch() error {
	return cascade.Wrap(cascade.KindPermissionDenied, ErrStateMismatch,
		"secrets: refusing an OAuth callback that this process did not initiate")
}

func errOAuthStateExpired() error {
	return cascade.Wrap(cascade.KindTimeout, ErrStateExpired,
		"secrets: refusing an OAuth callback whose authorization window has closed")
}

func errOAuthPKCEMismatch(cause error) error {
	return cascade.Wrap(cascade.KindPermissionDenied, errors.Join(ErrPKCEMismatch, cause),
		"secrets: the PKCE code verifier did not match the challenge sent at authorization")
}

func errOAuthCallbackTimeout(cause error) error {
	return cascade.Wrap(cascade.KindTimeout, errors.Join(ErrCallbackTimeout, cause),
		"secrets: the OAuth authorization deadline passed with no callback")
}

// errOAuthMalformedCallback names WHAT was wrong with the query - a
// duplicated parameter, an oversized code - and never quotes the query
// itself, because the query is where the authorization code lives.
func errOAuthMalformedCallback(reason string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrMalformedCallback,
		"secrets: refusing the OAuth callback: %s", reason)
}

func errOAuthMalformedTokenResponse(reason string) error {
	return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedTokenResponse,
		"secrets: refusing the OAuth token response: %s", reason)
}

// errOAuthAuthorizationDenied carries the authorization server's own error
// CODE (a fixed RFC 6749 §4.1.2.1 token such as "access_denied"), never its
// error_description, which is attacker-influenced free text.
func errOAuthAuthorizationDenied(code string) error {
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrAuthorizationDenied,
		"secrets: the authorization server returned error=%q", code)
}

func errOAuthInvalidGrant() error {
	return cascade.Wrap(cascade.KindPermissionDenied, ErrInvalidGrant,
		"secrets: the authorization server rejected the presented grant")
}

func errOAuthTokenExpired(providerID, account string) error {
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrTokenExpired,
		"secrets: the %s access token for account %q expired and no refresh token is stored", providerID, account)
}

func errOAuthGrantRevoked(providerID, account string) error {
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrGrantRevoked,
		"secrets: the %s grant for account %q was revoked; the stored credential has been removed", providerID, account)
}

func errOAuthCredentialInconsistent(providerID, account string, cause error) error {
	return cascade.Wrapf(cascade.KindIntegrity, errors.Join(ErrCredentialInconsistent, cause),
		"secrets: the %s credential for account %q could not be updated; it was left at its previous value", providerID, account)
}

func errOAuthExchangeFailed(status int) error {
	return cascade.Newf(cascade.KindUnavailable,
		"secrets: the OAuth token endpoint answered HTTP %d", status)
}
