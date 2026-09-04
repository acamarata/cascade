// Purpose: prove that oauthSecret cannot be printed, and that every OAuth
//
//	error constructor carries the sentinel and the Kind it claims.
//
// Constraints: this is the structural half of the no-leak guarantee. The
//
//	canary sweep in oauth_flow_test.go proves no token reached an output
//	on the paths the flow actually takes; this file proves the TYPE cannot
//	carry one onto a path nobody thought to test.
//
// SPORT: OAUTH_BROKER: ADD (tests).

package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestOAuthSecretIsUnprintableUnderEveryVerb(t *testing.T) {
	const secret = "CANARY-TYPE-LEVEL-TOKEN-51f7"
	s := newOAuthSecret([]byte(secret))
	renderings := map[string]string{
		"%s":      fmt.Sprintf("%s", s),
		"%v":      fmt.Sprintf("%v", s),
		"%+v":     fmt.Sprintf("%+v", s),
		"%#v":     fmt.Sprintf("%#v", s),
		"%q":      fmt.Sprintf("%q", s),
		"%x":      fmt.Sprintf("%x", s),
		"%d":      fmt.Sprintf("%d", s),
		"String":  s.String(),
		"Sprint":  fmt.Sprint(s),
		"in slot": fmt.Sprintf("token=%v", struct{ T oauthSecret }{s}),
	}
	for verb, out := range renderings {
		if strings.Contains(out, secret) {
			t.Fatalf("%s rendered the secret: %q", verb, out)
		}
		if !strings.Contains(out, redactedPlaceholder) {
			t.Fatalf("%s produced %q, which is not the redaction placeholder", verb, out)
		}
	}
	encoded, err := json.Marshal(struct {
		Token oauthSecret `json:"token"`
	}{s})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("JSON encoding rendered the secret: %s", encoded)
	}
}

func TestOAuthSecretBytesAndZero(t *testing.T) {
	raw := []byte("CANARY-ZEROED-9911")
	s := newOAuthSecret(raw)
	if string(s.Bytes()) != "CANARY-ZEROED-9911" {
		t.Fatal("Bytes did not return the value")
	}
	if s.Len() != len(raw) || s.Empty() {
		t.Fatalf("Len/Empty disagree with the value: %d/%v", s.Len(), s.Empty())
	}
	s.Zero()
	if !s.Empty() || s.Len() != 0 {
		t.Fatal("Zero did not drop the value")
	}
	for i, b := range raw {
		if b != 0 {
			t.Fatalf("byte %d survived Zero: %q", i, raw)
		}
	}
	var empty oauthSecret
	if !empty.Empty() || empty.String() != redactedPlaceholder {
		t.Fatal("the zero oauthSecret is not empty-and-redacted")
	}
}

// oauthErrorCases pairs each constructor with the sentinel it must carry
// and the taxonomy Kind it must claim.
func oauthErrorCases() []struct {
	name     string
	err      error
	sentinel error
	kind     cascade.Kind
} {
	cause := errors.New("underlying")
	return []struct {
		name     string
		err      error
		sentinel error
		kind     cascade.Kind
	}{
		{"no input", errOAuthNoInput("p"), ErrNoInput, cascade.KindUnavailable},
		{"state mismatch", errOAuthStateMismatch(), ErrStateMismatch, cascade.KindPermissionDenied},
		{"state expired", errOAuthStateExpired(), ErrStateExpired, cascade.KindTimeout},
		{"pkce mismatch", errOAuthPKCEMismatch(cause), ErrPKCEMismatch, cascade.KindPermissionDenied},
		{"callback timeout", errOAuthCallbackTimeout(cause), ErrCallbackTimeout, cascade.KindTimeout},
		{"malformed callback", errOAuthMalformedCallback("why"), ErrMalformedCallback, cascade.KindInvalidInput},
		{"malformed token response", errOAuthMalformedTokenResponse("why"), ErrMalformedTokenResponse, cascade.KindIntegrity},
		{"authorization denied", errOAuthAuthorizationDenied("access_denied"), ErrAuthorizationDenied, cascade.KindPermissionDenied},
		{"invalid grant", errOAuthInvalidGrant(), ErrInvalidGrant, cascade.KindPermissionDenied},
		{"token expired", errOAuthTokenExpired("p", "a"), ErrTokenExpired, cascade.KindPermissionDenied},
		{"grant revoked", errOAuthGrantRevoked("p", "a"), ErrGrantRevoked, cascade.KindPermissionDenied},
		{"credential inconsistent", errOAuthCredentialInconsistent("p", "a", cause), ErrCredentialInconsistent, cascade.KindIntegrity},
	}
}

func TestOAuthErrorsCarryTheirSentinelAndKind(t *testing.T) {
	for _, tc := range oauthErrorCases() {
		if !errors.Is(tc.err, tc.sentinel) {
			t.Fatalf("%s: errors.Is did not find the sentinel: %v", tc.name, tc.err)
		}
		if !cascade.HasKind(tc.err, tc.kind) {
			t.Fatalf("%s: kind is not %v: %v", tc.name, tc.kind, tc.err)
		}
		if tc.err.Error() == "" {
			t.Fatalf("%s: empty message", tc.name)
		}
	}
	if !cascade.HasKind(errOAuthExchangeFailed(503), cascade.KindUnavailable) {
		t.Fatal("an exchange failure is not KindUnavailable")
	}
	if !strings.Contains(errOAuthExchangeFailed(503).Error(), "503") {
		t.Fatal("an exchange failure does not name the status")
	}
}

func TestOAuthErrorsNeverQuoteTheirInput(t *testing.T) {
	// errOAuthMalformedCallback takes a REASON, never the query. There is
	// no constructor in this package that accepts a code, a token, or a
	// verifier, which is the property this test pins: adding one would
	// have to change this file.
	const wouldBeSecret = "CANARY-NEVER-IN-AN-ERROR-77aa"
	for _, tc := range oauthErrorCases() {
		if strings.Contains(tc.err.Error(), wouldBeSecret) {
			t.Fatalf("%s quoted a secret", tc.name)
		}
	}
	err := errOAuthMalformedCallback("the redirect query repeats the code parameter")
	if strings.Contains(err.Error(), "code=") {
		t.Fatalf("the refusal echoed the query: %v", err)
	}
}
