//go:build integration

// Purpose: drive refresh, revoke, error statuses, redirects and callback
//
//	paths through the PRODUCTION httpExchanger and a REAL bound loopback
//	listener against an httptest IdP with its own request counters. Every
//	test runs a real authorization first, so each one binds a real
//	127.0.0.1 port; a test that never binds leaves the counters at zero.
//
// Constraints: integration tag (net, net/http); in-memory custody only.
// SPORT: OAUTH_BROKER: ADD (real-IdP integration tests).

package secrets

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// realIDP is a token + revocation endpoint with per-path hit counters.
// mode picks how a refresh_token grant is answered.
type realIDP struct {
	srv     *httptest.Server
	mu      sync.Mutex
	hits    map[string]int
	gen     int
	refresh string
	lastRev url.Values
	mode    string // "rotate", "revoked", "redirect", "oversized"
	target  string // redirect Location
}

func newRealIDP(t *testing.T) *realIDP {
	t.Helper()
	idp := &realIDP{hits: map[string]int{}, mode: "rotate"}
	idp.srv = httptest.NewServer(http.HandlerFunc(idp.serve))
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *realIDP) count(path string) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.hits[path]
}

func (i *realIDP) set(mode, target, refresh string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.mode, i.target, i.refresh = mode, target, refresh
}

func (i *realIDP) revoked() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastRev
}

func (i *realIDP) serve(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hits[r.URL.Path]++
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/revoke" {
		i.lastRev = r.PostForm
		return
	}
	switch grant := r.PostForm.Get("grant_type"); {
	case grant == "authorization_code":
		i.issue(w)
	case grant != "refresh_token" || i.mode == "revoked" || r.PostForm.Get("refresh_token") != i.refresh:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	case i.mode == "redirect":
		w.Header().Set("Location", i.target)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	case i.mode == "oversized":
		pad := strings.Repeat("p", maxTokenResponseBytes+16)
		_, _ = w.Write([]byte(`{"access_token":"big","refresh_token":"big","pad":"` + pad + `"}`))
	default:
		i.issue(w)
	}
}

// issue mints generation gen+1 and remembers its refresh value, so a
// refresh that presents anything but the latest value is invalid_grant.
func (i *realIDP) issue(w http.ResponseWriter) {
	i.gen++
	n := strconv.Itoa(i.gen)
	i.refresh = "idp-refresh-" + n
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"idp-access-` + n + `","refresh_token":"` + i.refresh + `","token_type":"bearer"}`))
}

// realBroker builds the production broker against idp. browse is what the
// "browser" does with the authorization URL.
func realBroker(t *testing.T, idp *realIDP, browse func(ctx context.Context, redirect, state string) error) (*OAuthBroker, *Broker, *memCustody) {
	t.Helper()
	vault, custody := newTestBroker(t, &allowGate{})
	cfg := testOAuthConfig()
	cfg.AuthEndpoint, cfg.TokenEndpoint, cfg.RevocationEndpoint = idp.srv.URL+"/authorize", idp.srv.URL+"/token", idp.srv.URL+"/revoke"
	broker, err := NewOAuthBroker(cfg, OAuthDeps{Vault: vault, Clock: fixedClock{at: time.Unix(1_700_000_000, 0).UTC()}, Deadline: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewOAuthBroker: %v", err)
	}
	broker.open = func(ctx context.Context, rawURL string) error {
		parsed, perr := url.Parse(rawURL)
		if perr != nil {
			return perr
		}
		return browse(ctx, parsed.Query().Get("redirect_uri"), parsed.Query().Get("state"))
	}
	return broker, vault, custody
}

// browserGet performs one real request against the bound listener.
func browserGet(ctx context.Context, method, target string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	return resp.StatusCode, resp.Body.Close()
}

func honestBrowser(ctx context.Context, redirect, state string) error {
	_, err := browserGet(ctx, http.MethodGet, redirect+"?code=real-code&state="+url.QueryEscape(state))
	return err
}

// grantOverRealSockets runs Start over the real listener and returns the
// stored record.
func grantOverRealSockets(t *testing.T, idp *realIDP) (*OAuthBroker, *Broker, *memCustody, storedRecord) {
	t.Helper()
	broker, vault, custody := realBroker(t, idp, honestBrowser)
	if _, err := broker.Start(context.Background()); err != nil {
		t.Fatalf("Start over real sockets: %v", err)
	}
	rec, err := broker.store.load(context.Background(), defaultAccount)
	if err != nil || idp.count("/token") != 1 {
		t.Fatalf("grant: load err %v, token hits %d", err, idp.count("/token"))
	}
	return broker, vault, custody, rec
}

func assertVaultUnchanged(t *testing.T, b *OAuthBroker, c *memCustody, want storedRecord) {
	t.Helper()
	got, err := b.store.load(context.Background(), defaultAccount)
	if err != nil || got.AccessRef != want.AccessRef || got.RefreshRef != want.RefreshRef || got.Gen != want.Gen {
		t.Fatalf("the stored record moved: %+v (err %v), want %+v", got, err, want)
	}
	if string(c.entries[want.AccessRef]) != "idp-access-1" || string(c.entries[want.RefreshRef]) != "idp-refresh-1" {
		t.Fatal("the stored token values changed")
	}
}

func TestOAuthTokenEndpointRedirectNotFollowed_Integration(t *testing.T) {
	idp := newRealIDP(t)
	second := newRealIDP(t)
	broker, _, custody, before := grantOverRealSockets(t, idp)
	second.set("rotate", "", "idp-refresh-1")
	idp.set("redirect", second.srv.URL+"/token", "idp-refresh-1")
	_, err := broker.Refresh(context.Background(), defaultAccount)
	if !errors.Is(err, errOAuthRedirectRefused) || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Refresh over a 307 = %v, want the redirect refusal naming HTTP 307", err)
	}
	if n := second.count("/token"); n != 0 {
		t.Fatalf("the redirect target received %d requests; the refresh token was re-sent", n)
	}
	if idp.count("/token") != 2 {
		t.Fatalf("token endpoint hits %d, want 2", idp.count("/token"))
	}
	assertVaultUnchanged(t, broker, custody, before)
}

func TestOAuthRefreshRotatesOverRealTransport_Integration(t *testing.T) {
	idp := newRealIDP(t)
	broker, _, custody, first := grantOverRealSockets(t, idp)
	for gen := 2; gen <= 3; gen++ {
		if _, err := broker.Refresh(context.Background(), defaultAccount); err != nil {
			t.Fatalf("Refresh to generation %d: %v", gen, err)
		}
		rec, err := broker.store.load(context.Background(), defaultAccount)
		if err != nil || rec.Gen != gen || !strings.HasSuffix(rec.RefreshRef, "."+strconv.Itoa(gen)) {
			t.Fatalf("after refresh %d the record is %+v (err %v)", gen, rec, err)
		}
		if string(custody.entries[rec.RefreshRef]) != "idp-refresh-"+strconv.Itoa(gen) {
			t.Fatal("the rotated refresh value is not the one stored")
		}
		if hasAnyEntry(custody, first.AccessRef, first.RefreshRef) {
			t.Fatal("a superseded access or refresh entry is still in the vault")
		}
		first = rec
	}
	if idp.count("/token") != 3 {
		t.Fatalf("token endpoint hits %d, want 3 (grant + two refreshes)", idp.count("/token"))
	}
}

func TestOAuthRefreshInvalidGrantPurges_Integration(t *testing.T) {
	idp := newRealIDP(t)
	broker, vault, custody, rec := grantOverRealSockets(t, idp)
	idp.set("revoked", "", "idp-refresh-1")
	_, err := broker.Refresh(context.Background(), defaultAccount)
	if !errors.Is(err, ErrGrantRevoked) || !strings.Contains(err.Error(), "was revoked") {
		t.Fatalf("Refresh on invalid_grant = %v, want the grant-revoked refusal", err)
	}
	key := broker.store.recordKey(defaultAccount)
	if _, gerr := vault.Get(context.Background(), key); gerr == nil || gerr.Error() != ErrSecretNotFound(key).Error() {
		t.Fatalf("the record survived a revoked grant: %v", gerr)
	}
	if kept := hasAnyEntry(custody, rec.AccessRef, rec.RefreshRef); kept || idp.count("/token") != 2 {
		t.Fatalf("token entries kept=%v, token hits %d", kept, idp.count("/token"))
	}
}

func TestOAuthCallbackStateMismatchOverRealListener_Integration(t *testing.T) {
	idp := newRealIDP(t)
	broker, _, _ := realBroker(t, idp, func(ctx context.Context, redirect, _ string) error {
		return honestBrowser(ctx, redirect, "forged-state")
	})
	_, err := broker.Start(context.Background())
	if !errors.Is(err, ErrStateMismatch) || !strings.Contains(err.Error(), "did not initiate") {
		t.Fatalf("Start with a forged state = %v, want the state refusal", err)
	}
	if idp.count("/token") != 0 {
		t.Fatalf("a forged callback reached the token endpoint %d times", idp.count("/token"))
	}
}

func TestOAuthCallbackIgnoresForeignPath_Integration(t *testing.T) {
	idp := newRealIDP(t)
	var strays []int
	broker, _, _ := realBroker(t, idp, func(ctx context.Context, redirect, state string) error {
		base := strings.TrimSuffix(redirect, "/callback")
		for _, probe := range [][2]string{{http.MethodGet, base + "/favicon.ico"}, {http.MethodPost, redirect}, {http.MethodHead, redirect}} {
			status, err := browserGet(ctx, probe[0], probe[1])
			if err != nil {
				return err
			}
			strays = append(strays, status)
		}
		return honestBrowser(ctx, redirect, state)
	})
	if _, err := broker.Start(context.Background()); err != nil {
		t.Fatalf("a stray request broke the flow: %v", err)
	}
	for i, status := range strays {
		if status != http.StatusNotFound {
			t.Fatalf("stray request %d answered %d, want 404", i, status)
		}
	}
	if len(strays) != 3 || idp.count("/token") != 1 {
		t.Fatalf("strays %v, token hits %d", strays, idp.count("/token"))
	}
}

func TestOAuthOversizedTokenResponseRefused_Integration(t *testing.T) {
	idp := newRealIDP(t)
	broker, _, custody, before := grantOverRealSockets(t, idp)
	idp.set("oversized", "", "idp-refresh-1")
	_, err := broker.Refresh(context.Background(), defaultAccount)
	if !errors.Is(err, errOAuthResponseTooLarge) || !strings.Contains(err.Error(), strconv.Itoa(maxTokenResponseBytes)) {
		t.Fatalf("Refresh over an oversized body = %v, want the size refusal naming the cap", err)
	}
	if idp.count("/token") != 2 {
		t.Fatalf("token endpoint hits %d, want 2", idp.count("/token"))
	}
	assertVaultUnchanged(t, broker, custody, before)
}

func TestOAuthRevokeOverRealTransport_Integration(t *testing.T) {
	idp := newRealIDP(t)
	broker, vault, _, _ := grantOverRealSockets(t, idp)
	if err := broker.Revoke(context.Background(), defaultAccount); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if form := idp.revoked(); idp.count("/revoke") != 1 || form.Get("token") != "idp-refresh-1" || form.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revocation hits %d, form keys %d", idp.count("/revoke"), len(form))
	}
	key := broker.store.recordKey(defaultAccount)
	if _, err := vault.Get(context.Background(), key); err == nil || err.Error() != ErrSecretNotFound(key).Error() {
		t.Fatalf("the record survived Revoke: %v", err)
	}
}
