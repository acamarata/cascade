// Purpose: the socket-free half of the OAuth broker's protocol - PKCE
//
//	verifier/challenge generation, the single-use state table that is the
//	broker's CSRF defence, the authorization URL builder, and the
//	decoders for the two attacker-influenced inputs in the flow (the
//	loopback redirect query and the token-endpoint JSON body).
//
// Inputs: an injected io.Reader for entropy (crypto/rand in production, a
//
//	deterministic reader in tests) and raw bytes from the far end. This
//	file opens no socket and imports neither "net" nor "net/http", so the
//	whole protocol contract is asserted in the default unit lane -
//	internal/client's split, applied here.
//
// Outputs: pkceCodes, authSession, oauthCallback, tokenResponse - every
//
//	credential-bearing field an oauthSecret.
//
// Constraints: 06-FORGE-SPEC §5.7 - both decoders here are parsers over
//
//	untrusted input and are fuzzed (oauth_fuzz_test.go). Nothing in this
//	file writes to a log, and no error it returns quotes the input it
//	rejected, because the input is where the authorization code lives.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets PKCE core).

package secrets

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Parameter size caps. An authorization code is an opaque server-chosen
// string; every real one is well under a hundred bytes. The caps exist so a
// hostile redirect cannot make the broker allocate without bound, and so an
// "oversized code" is a refusal rather than a value that flows onward.
const (
	maxCallbackQueryLen = 8192
	maxCallbackCodeLen  = 2048
	maxErrorCodeLen     = 64
	// pkceEntropyBytes is 32 raw bytes, which base64url-encodes to 43
	// characters - the RFC 7636 §4.1 recommended verifier length, and the
	// maximum entropy the 43..128 character window allows.
	pkceEntropyBytes = 32
	// stateEntropyBytes matches the verifier: state is a CSRF token, and a
	// guessable one is the same defect as a guessable verifier.
	stateEntropyBytes = 32
)

// pkceCodes is one authorization's PKCE pair. The verifier is credential
// material (anyone holding it can complete an intercepted authorization),
// so it is an oauthSecret; the challenge is the public SHA-256 digest that
// travels in the authorization URL.
type pkceCodes struct {
	verifier  oauthSecret
	challenge string
}

// newPKCECodes draws a fresh verifier from r. It is called exactly once per
// authorization: reusing a verifier across two authorizations would let a
// party who observed the first complete the second.
func newPKCECodes(r io.Reader) (pkceCodes, error) {
	raw := make([]byte, pkceEntropyBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return pkceCodes{}, cascade.Wrap(cascade.KindUnavailable, err,
			"secrets: could not draw PKCE verifier entropy")
	}
	verifier := []byte(base64.RawURLEncoding.EncodeToString(raw))
	for i := range raw {
		raw[i] = 0
	}
	return pkceCodes{verifier: newOAuthSecret(verifier), challenge: pkceChallenge(verifier)}, nil
}

// pkceChallenge is the RFC 7636 §4.2 S256 transformation. The "plain"
// method is not implemented anywhere in this package: sending the verifier
// as its own challenge makes the mechanism a no-op against anyone who can
// read the authorization request, which on a shared machine is the browser
// history and the process table.
func pkceChallenge(verifier []byte) string {
	sum := sha256.Sum256(verifier)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// pkceVerify reports whether verifier hashes to challenge, comparing in
// constant time. Production cascade is the CLIENT and never runs this
// check - the authorization server does - but the mock IdP in the tests
// does, which is what turns "PKCE is implemented" into "a mismatched
// verifier is provably rejected".
func pkceVerify(verifier []byte, challenge string) bool {
	return subtle.ConstantTimeCompare([]byte(pkceChallenge(verifier)), []byte(challenge)) == 1
}

// newState draws a fresh CSRF state value.
func newState(r io.Reader) (string, error) {
	raw := make([]byte, stateEntropyBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not draw OAuth state entropy")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// authSession is one in-flight authorization. It lives only in memory and
// only until its callback is consumed or its deadline passes: nothing here
// is written to disk, because a persisted verifier would outlive the
// window it protects.
type authSession struct {
	state       string
	codes       pkceCodes
	redirectURI string
	expiresAt   time.Time
}

// authURL builds the authorization endpoint URL. The verifier never appears
// here - only its challenge, which is the entire point of PKCE.
func (s *authSession) authURL(endpoint, clientID string, scopes []string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "secrets: auth_endpoint %q is not a URL", endpoint)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", s.redirectURI)
	q.Set("state", s.state)
	q.Set("code_challenge", s.codes.challenge)
	q.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// oauthCallback is a parsed redirect query. Code is an oauthSecret because
// an authorization code is a single-use credential: anyone who obtains it
// before cascade redeems it can redeem it instead.
type oauthCallback struct {
	code      oauthSecret
	state     string
	errorCode string
}

// parseOAuthCallback decodes the loopback redirect's raw query string. It
// is fail-closed at every step, and in particular it refuses a DUPLICATED
// code or state rather than picking one: a query with two states lets an
// intermediary pair the operator's session with the attacker's code, and
// "take the first" versus "take the last" is exactly the ambiguity such an
// attack needs. No error message quotes the query.
func parseOAuthCallback(rawQuery string) (oauthCallback, error) {
	if len(rawQuery) > maxCallbackQueryLen {
		return oauthCallback{}, errOAuthMalformedCallback("the redirect query exceeds the size limit")
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return oauthCallback{}, errOAuthMalformedCallback("the redirect query is not valid form encoding")
	}
	for _, key := range []string{"code", "state", "error"} {
		if len(values[key]) > 1 {
			return oauthCallback{}, errOAuthMalformedCallback("the redirect query repeats the " + key + " parameter")
		}
	}
	state := values.Get("state")
	if state == "" {
		return oauthCallback{}, errOAuthMalformedCallback("the redirect query carries no state parameter")
	}
	if code := values.Get("error"); code != "" {
		if len(code) > maxErrorCodeLen || !isRFC6749ErrorCode(code) {
			return oauthCallback{}, errOAuthMalformedCallback("the redirect query carries an unusable error code")
		}
		return oauthCallback{state: state, errorCode: code}, nil
	}
	return parseCallbackCode(values.Get("code"), state)
}

// parseCallbackCode validates the authorization code half of a successful
// callback.
func parseCallbackCode(code, state string) (oauthCallback, error) {
	switch {
	case code == "":
		return oauthCallback{}, errOAuthMalformedCallback("the redirect query carries neither a code nor an error")
	case len(code) > maxCallbackCodeLen:
		return oauthCallback{}, errOAuthMalformedCallback("the authorization code exceeds the size limit")
	}
	return oauthCallback{code: newOAuthSecret([]byte(code)), state: state}, nil
}

// isRFC6749ErrorCode reports whether s is drawn from the RFC 6749 §4.1.2.1
// error-code character set (%x20-21 / %x23-5B / %x5D-7E). Codes that pass
// this check are safe to place in an error message; error_description,
// which is free text the server chooses, is never surfaced at all.
func isRFC6749ErrorCode(s string) bool {
	for _, r := range s {
		if r == 0x22 || r == 0x5C || r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

// tokenResponse is a decoded token-endpoint body.
type tokenResponse struct {
	access    oauthSecret
	refresh   oauthSecret
	scope     string
	expiresIn int64
}

// tokenResponseWire is the JSON shape. Its token fields land as Go strings,
// which are immutable and therefore cannot be zeroed - an irreducible cost
// of decoding JSON with encoding/json, named here rather than papered over
// with a zeroing call that would not do anything. decodeTokenResponse zeroes
// the response BUFFER, which it does own, and moves the values into
// oauthSecret immediately so nothing downstream can format them.
type tokenResponseWire struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

// decodeTokenResponse parses a token-endpoint body. An error= body becomes
// a typed refusal; "invalid_grant" specifically becomes ErrInvalidGrant,
// which the CALLER disambiguates, because RFC 7636 §4.6 gives that one code
// two very different meanings depending on which exchange raised it: during
// Start it means the PKCE verifier did not match, during Refresh it means
// the grant was revoked underneath cascade. Deciding here would have to
// guess; deciding at the call site does not.
func decodeTokenResponse(body []byte) (tokenResponse, error) {
	defer func() {
		for i := range body {
			body[i] = 0
		}
	}()
	var wire tokenResponseWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return tokenResponse{}, errOAuthMalformedTokenResponse("the body is not a JSON object")
	}
	if wire.Error != "" {
		return tokenResponse{}, tokenEndpointError(wire.Error)
	}
	switch {
	case wire.AccessToken == "":
		return tokenResponse{}, errOAuthMalformedTokenResponse("the body carries no access_token")
	case wire.TokenType != "" && !strings.EqualFold(wire.TokenType, "bearer"):
		return tokenResponse{}, errOAuthMalformedTokenResponse("the body declares an unsupported token_type")
	case wire.ExpiresIn < 0:
		return tokenResponse{}, errOAuthMalformedTokenResponse("the body declares a negative expires_in")
	}
	out := tokenResponse{
		access:    newOAuthSecret([]byte(wire.AccessToken)),
		scope:     wire.Scope,
		expiresIn: wire.ExpiresIn,
	}
	if wire.RefreshToken != "" {
		out.refresh = newOAuthSecret([]byte(wire.RefreshToken))
	}
	return out, nil
}

// tokenEndpointError classifies an RFC 6749 §5.2 error code.
func tokenEndpointError(code string) error {
	if len(code) > maxErrorCodeLen || !isRFC6749ErrorCode(code) {
		return errOAuthMalformedTokenResponse("the body carries an unusable error code")
	}
	if code == "invalid_grant" {
		return errOAuthInvalidGrant()
	}
	return errOAuthAuthorizationDenied(code)
}
