// Purpose: the vault-persistence and entropy-failure paths - the branches
//
//	that only run when the store or the entropy source misbehaves, which
//	is exactly where a half-updated credential would appear.
//
// Constraints: same no-network rule as oauth_test.go.
// SPORT: OAUTH_BROKER: ADD (tests).

package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failingReader is an entropy source that refuses. A verifier that cannot
// be drawn must abort the authorization, never fall back to something
// weaker.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy available") }

func TestOAuthAbortsWhenEntropyIsUnavailable(t *testing.T) {
	if _, err := newPKCECodes(failingReader{}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a failed verifier draw was not KindUnavailable: %v", err)
	}
	if _, err := newState(failingReader{}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a failed state draw was not KindUnavailable: %v", err)
	}
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	h.broker.rand = failingReader{}
	if _, err := h.broker.Start(context.Background()); err == nil {
		t.Fatal("Start proceeded with no entropy")
	}
	if h.idp.callCount() != 0 {
		t.Fatal("a flow with no verifier still reached the token endpoint")
	}
}

func TestOAuthAbortsWhenTheAuthEndpointIsUnusable(t *testing.T) {
	cfg := testOAuthConfig()
	h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
	// The endpoint passed Validate at construction; corrupt it afterwards
	// so the URL builder's own refusal path runs.
	h.broker.cfg.AuthEndpoint = "https://idp.example/\x7f\x00"
	if _, err := h.broker.Start(context.Background()); err == nil {
		t.Fatal("an unparsable authorization endpoint was accepted")
	}
}

func TestOAuthRefusesAnUnusableAccountForRedirectBinding(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	h.listener.port = ""
	if _, err := h.broker.Start(context.Background()); err == nil {
		t.Fatal("a listener that reported no port still produced a redirect URI")
	}
}

func TestOAuthStoreRefusesAnEmptyRefAndAMissingRecord(t *testing.T) {
	h, _ := grantedHarness(t)
	if _, err := h.broker.store.token(context.Background(), ""); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("an empty ref was not a not-found refusal: %v", err)
	}
	if _, err := h.broker.store.load(context.Background(), "never-granted"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("loading an unknown account was not a not-found refusal: %v", err)
	}
	if err := h.broker.Revoke(context.Background(), "never-granted"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("revoking an unknown account was not a not-found refusal: %v", err)
	}
}

func TestOAuthStoreRefusesACorruptRecord(t *testing.T) {
	h, _ := grantedHarness(t)
	h.custody.entries[h.broker.store.recordKey(defaultAccount)] = []byte("{not json")
	_, err := h.broker.store.load(context.Background(), defaultAccount)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("a corrupt record was not an integrity refusal: %v", err)
	}
	if strings.Contains(err.Error(), "{not json") {
		t.Fatalf("the refusal echoed the stored bytes: %v", err)
	}
}

func TestOAuthCommitFailureIsReportedAsInconsistentNotAsSuccess(t *testing.T) {
	h, first := grantedHarness(t)
	h.custody.failOn, h.custody.err = "set", errors.New("store is read-only")
	_, err := h.broker.Refresh(context.Background(), defaultAccount)
	if !errors.Is(err, ErrCredentialInconsistent) {
		t.Fatalf("a failed commit was not reported as inconsistent: %v", err)
	}
	h.custody.failOn = ""
	after, loadErr := h.broker.store.load(context.Background(), defaultAccount)
	if loadErr != nil {
		t.Fatalf("load after the failed commit: %v", loadErr)
	}
	if after.AccessRef != first.AccessRef {
		t.Fatal("the failed commit still moved the record")
	}
}

func TestOAuthPurgeReportsAPartialRemoval(t *testing.T) {
	h, first := grantedHarness(t)
	h.custody.failOn, h.custody.err = "delete", errors.New("store is read-only")
	err := h.broker.Revoke(context.Background(), defaultAccount)
	if err == nil {
		t.Fatal("a purge that removed nothing reported success")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a partial purge was not KindUnavailable: %v", err)
	}
	if _, exists := h.custody.entries[first.AccessRef]; !exists {
		t.Fatal("the test's premise is wrong: the entry was removed after all")
	}
}

func TestOAuthCommitKeepsThePreviousRefreshRefWhenNoneIsReturned(t *testing.T) {
	h, first := grantedHarness(t)
	// A provider that does not rotate refresh tokens returns none on
	// refresh. The record must keep pointing at the one it already has,
	// not silently drop the ability to refresh again.
	h.idp.body = `{"access_token":"` + canaryAccess + `","token_type":"Bearer","expires_in":3600}`
	h.idp.status = 200
	second, err := h.broker.Refresh(context.Background(), defaultAccount)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if second.RefreshRef != first.RefreshRef {
		t.Fatalf("the refresh ref moved to %q although no new refresh token was issued", second.RefreshRef)
	}
	if string(h.custody.entries[first.RefreshRef]) != canaryRefresh {
		t.Fatal("the still-referenced refresh token was deleted as if superseded")
	}
	if second.Scopes == nil || len(second.Scopes) != len(first.Scopes) {
		t.Fatalf("granted scopes were dropped on refresh: %v", second.Scopes)
	}
}

func TestOAuthCallbackRejectsControlCharactersInTheErrorCode(t *testing.T) {
	for _, query := range []string{
		"error=access\x00denied&state=s",
		"error=\"quoted\"&state=s",
		"error=back\\slash&state=s",
		"error=" + strings.Repeat("e", maxErrorCodeLen+1) + "&state=s",
	} {
		if _, err := parseOAuthCallback(query); !errors.Is(err, ErrMalformedCallback) {
			t.Fatalf("query %q was accepted: %v", query, err)
		}
	}
	callback, err := parseOAuthCallback("error=access_denied&state=s")
	if err != nil {
		t.Fatalf("a well-formed denial was refused: %v", err)
	}
	if callback.errorCode != "access_denied" || !callback.code.Empty() {
		t.Fatalf("denial parsed as %+v", callback)
	}
}

func TestOAuthStoreKeyNamesAreVaultLegal(t *testing.T) {
	store := oauthStore{providerID: "acme.idp-1"}
	names := []string{
		store.recordKey("default"),
		store.tokenKey("default", "access", 1),
		store.tokenKey("default", "refresh", 42),
	}
	for _, name := range names {
		if err := validateSecretName(name); err != nil {
			t.Fatalf("key %q is not a legal vault name: %v", name, err)
		}
		if !strings.HasPrefix(name, oauthKeyPrefix) {
			t.Fatalf("key %q is outside the oauth namespace", name)
		}
	}
	rec := storedRecord{TokenRecord: provider.TokenRecord{ExpiresAt: time.Unix(10, 0)}, Gen: 3}
	if rec.Gen != 3 || rec.ExpiresAt.IsZero() {
		t.Fatal("the stored record lost its fields")
	}
}
