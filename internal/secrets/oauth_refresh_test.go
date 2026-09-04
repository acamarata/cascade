// Purpose: the credential-lifetime tests - single-flight refresh, expiry,
//
//	the revoked-grant purge, and the "a failed refresh leaves the previous
//	credential intact" property.
//
// Constraints: same no-network rule as oauth_test.go.
// SPORT: OAUTH_BROKER: ADD (tests).

package secrets

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// grantedHarness runs one authorization so a credential exists to refresh.
func grantedHarness(t *testing.T) (*oauthHarness, provider.TokenRecord) {
	t.Helper()
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h, rec
}

func TestOAuthSingleFlightRefreshIssuesExactlyOneRequest(t *testing.T) {
	h, _ := grantedHarness(t)
	// The leader's exchange is held open until every follower has joined,
	// so the collapse is proven rather than won by the scheduler.
	const callers = 10
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	joined := make(chan struct{}, callers)
	h.broker.exchange = &gatedExchanger{inner: h.idp, arrived: arrived, release: release}
	h.broker.joined = joined

	var wg sync.WaitGroup
	results := make([]provider.TokenRecord, callers)
	errs := make([]error, callers)
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = h.broker.Refresh(context.Background(), defaultAccount)
		}()
	}
	start(0)
	<-arrived
	for i := 1; i < callers; i++ {
		start(i)
	}
	for i := 1; i < callers; i++ {
		<-joined
	}
	close(release)
	wg.Wait()

	if got := h.idp.callCount(); got != 2 {
		t.Fatalf("%d exchanges reached the IdP (1 authorization + 1 refresh expected), want 2", got)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d got an error: %v", i, errs[i])
		}
		if results[i].AccessRef != results[0].AccessRef {
			t.Fatalf("caller %d got a different record than the leader", i)
		}
	}
}

// gatedExchanger holds the first exchange open until release is closed, so
// every concurrent caller has definitely reached the single-flight gate.
type gatedExchanger struct {
	inner   tokenExchanger
	once    sync.Once
	arrived chan struct{}
	release chan struct{}
}

func (g *gatedExchanger) Exchange(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	g.once.Do(func() {
		g.arrived <- struct{}{}
		<-g.release
	})
	return g.inner.Exchange(ctx, endpoint, form)
}

func TestOAuthRefreshRotatesRefsAndRemovesTheOldGeneration(t *testing.T) {
	h, first := grantedHarness(t)
	second, err := h.broker.Refresh(context.Background(), defaultAccount)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if second.AccessRef == first.AccessRef {
		t.Fatal("the refreshed access token reused the previous generation's ref")
	}
	if _, exists := h.custody.entries[first.AccessRef]; exists {
		t.Fatal("the superseded access entry was left in the vault")
	}
	if string(h.custody.entries[second.AccessRef]) != canaryAccess {
		t.Fatal("the refreshed access token is not in the vault under its new ref")
	}
}

func TestOAuthRefreshFailureLeavesThePreviousCredentialIntact(t *testing.T) {
	h, first := grantedHarness(t)
	h.idp.failWith = errors.New("dial tcp: connection refused")
	if _, err := h.broker.Refresh(context.Background(), defaultAccount); err == nil {
		t.Fatal("a failed refresh reported success")
	}
	h.idp.failWith = nil
	after, err := h.broker.store.load(context.Background(), defaultAccount)
	if err != nil {
		t.Fatalf("loading the record after the failure: %v", err)
	}
	if after.AccessRef != first.AccessRef || after.RefreshRef != first.RefreshRef {
		t.Fatalf("the failed refresh moved the record: %+v, was %+v", after.TokenRecord, first)
	}
	if string(h.custody.entries[first.AccessRef]) != canaryAccess {
		t.Fatal("the previous access token is no longer usable after a failed refresh")
	}
}

func TestOAuthRevokedGrantPurgesRatherThanServingACachedToken(t *testing.T) {
	h, first := grantedHarness(t)
	h.idp.body, h.idp.status = `{"error":"invalid_grant"}`, 400
	_, err := h.broker.Refresh(context.Background(), defaultAccount)
	if !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("a revoked grant was not reported: %v", err)
	}
	if _, exists := h.custody.entries[first.AccessRef]; exists {
		t.Fatal("the access token survived a revoked grant; a later call would serve it")
	}
	if _, exists := h.custody.entries[first.RefreshRef]; exists {
		t.Fatal("the refresh token survived a revoked grant")
	}
	if _, err := h.broker.AccessToken(context.Background(), defaultAccount); err == nil {
		t.Fatal("AccessToken still answered after the grant was revoked")
	}
}

func TestOAuthAccessTokenRefreshesAnExpiredRecord(t *testing.T) {
	h, first := grantedHarness(t)
	// Move past the recorded expiry.
	h.broker.clock = fixedClock{at: first.ExpiresAt.Add(time.Hour)}
	token, err := h.broker.AccessToken(context.Background(), defaultAccount)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if string(token) != canaryAccess {
		t.Fatalf("AccessToken returned %q", token)
	}
	if h.idp.callCount() != 2 {
		t.Fatalf("expected the expired record to trigger exactly one refresh, saw %d exchanges", h.idp.callCount())
	}
}

func TestOAuthAccessTokenRefusesAnExpiredRecordWithNoRefreshToken(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	h.idp.body = `{"access_token":"` + canaryAccess + `","token_type":"Bearer","expires_in":1}`
	h.idp.status = 200
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rec.RefreshRef != "" {
		t.Fatal("a response with no refresh_token still recorded a refresh ref")
	}
	h.broker.clock = fixedClock{at: rec.ExpiresAt.Add(time.Hour)}
	if _, err := h.broker.AccessToken(context.Background(), defaultAccount); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("an unrefreshable expired token was not refused: %v", err)
	}
}

func TestOAuthAccessTokenServesAnUnexpiredRecordWithoutRefreshing(t *testing.T) {
	h, _ := grantedHarness(t)
	token, err := h.broker.AccessToken(context.Background(), defaultAccount)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if string(token) != canaryAccess {
		t.Fatalf("AccessToken returned %q", token)
	}
	if h.idp.callCount() != 1 {
		t.Fatalf("an unexpired token triggered a refresh (%d exchanges)", h.idp.callCount())
	}
}

func TestOAuthRefreshRefusesWithoutElevation(t *testing.T) {
	h, _ := grantedHarness(t)
	// Rebuild the store over a broker with no elevation gate: reading a
	// stored token is an elevated verb, so an unauthorised process must be
	// refused rather than handed a bearer credential.
	unelevated, err := NewBroker(h.custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	h.broker.store.vault = unelevated
	if _, err := h.broker.Refresh(context.Background(), defaultAccount); err == nil {
		t.Fatal("a refresh succeeded with no elevation gate configured")
	}
}

func TestOAuthRevokeRemovesEverythingAndReportsTheEndpoint(t *testing.T) {
	cfg := testOAuthConfig()
	cfg.RevocationEndpoint = "https://idp.example/revoke"
	h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.broker.Revoke(context.Background(), defaultAccount); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for _, ref := range []string{rec.AccessRef, rec.RefreshRef, h.broker.store.recordKey(defaultAccount)} {
		if _, exists := h.custody.entries[ref]; exists {
			t.Fatalf("%s survived Revoke", ref)
		}
	}
	if h.idp.callCount() != 2 {
		t.Fatalf("the revocation endpoint was not posted to (%d exchanges)", h.idp.callCount())
	}
}

func TestOAuthRevokePurgesEvenWhenTheEndpointFails(t *testing.T) {
	cfg := testOAuthConfig()
	cfg.RevocationEndpoint = "https://idp.example/revoke"
	h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.idp.status, h.idp.body = 503, `{}`
	err = h.broker.Revoke(context.Background(), defaultAccount)
	if err == nil {
		t.Fatal("a failed revocation was reported as success")
	}
	assertNoCanary(t, "the revocation error", err.Error())
	if _, exists := h.custody.entries[rec.RefreshRef]; exists {
		t.Fatal("a token cascade could not revoke remotely was left on this machine")
	}
}

func TestOAuthRefreshRejectsAnUnusableAccountName(t *testing.T) {
	h, _ := grantedHarness(t)
	for _, bad := range []string{"", strings.Repeat("a", maxSecretNameLen+1), "has space", "-flaglike"} {
		if _, err := h.broker.Refresh(context.Background(), bad); err == nil {
			t.Fatalf("account name %q was accepted", bad)
		}
		if err := h.broker.Revoke(context.Background(), bad); err == nil {
			t.Fatalf("Revoke accepted account name %q", bad)
		}
		if _, err := h.broker.AccessToken(context.Background(), bad); err == nil {
			t.Fatalf("AccessToken accepted account name %q", bad)
		}
	}
}
