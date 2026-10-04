//go:build integration

// Purpose: pin the token-endpoint and callback edges the real-IdP tests do
//
//	not reach: every redirect status is refused and leaves the vault alone,
//	the response size cap is exact, and near-miss callback paths cannot take
//	the listener's slot. Every test binds real 127.0.0.1 ports and asserts
//	the servers' own request counters.
//
// Constraints: integration tag (net, net/http); in-memory custody only.
// SPORT: OAUTH_BROKER: ADD (transport edge tests).

package secrets

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// hasAnyEntry reports whether custody still holds any of refs.
func hasAnyEntry(c *memCustody, refs ...string) bool {
	for _, ref := range refs {
		if _, ok := c.entries[ref]; ok {
			return true
		}
	}
	return false
}

// redirectingIDP wraps a realIDP: once armed, its token endpoint answers
// status with a Location on target and an invalid_grant body, the body a
// client that ignored the status would act on.
func redirectingIDP(t *testing.T, status int, target string) (*realIDP, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	armed, redirected := &atomic.Bool{}, &atomic.Int32{}
	idp := &realIDP{hits: map[string]int{}, mode: "rotate"}
	idp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !armed.Load() || r.URL.Path != "/token" {
			idp.serve(w, r)
			return
		}
		redirected.Add(1)
		w.Header().Set("Location", target)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(idp.srv.Close)
	return idp, armed, redirected
}

func TestOAuthTokenEndpointRedirectStatusesRefused_Integration(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusPermanentRedirect} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			second := newRealIDP(t)
			second.set("rotate", "", "idp-refresh-1")
			idp, armed, redirected := redirectingIDP(t, status, second.srv.URL+"/token")
			broker, _, custody, before := grantOverRealSockets(t, idp)
			armed.Store(true)
			_, err := broker.Refresh(context.Background(), defaultAccount)
			if !errors.Is(err, errOAuthRedirectRefused) || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("Refresh over a %d = %v, want the redirect refusal naming the status", status, err)
			}
			if redirected.Load() != 1 || second.count("/token") != 0 {
				t.Fatalf("redirects served %d, redirect target hits %d; want 1 and 0", redirected.Load(), second.count("/token"))
			}
			assertVaultUnchanged(t, broker, custody, before)
		})
	}
}

func TestOAuthExchangeResponseCapIsExact_Integration(t *testing.T) {
	var size atomic.Int64
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(size.Load())))
	}))
	t.Cleanup(srv.Close)
	exchanger := newHTTPExchanger()
	form := url.Values{"grant_type": {"refresh_token"}}

	size.Store(maxTokenResponseBytes)
	body, status, err := exchanger.Exchange(context.Background(), srv.URL, form)
	if err != nil || status != http.StatusOK || len(body) != maxTokenResponseBytes {
		t.Fatalf("a body of exactly the cap: len %d, status %d, err %v; want it accepted", len(body), status, err)
	}
	size.Store(maxTokenResponseBytes + 1)
	body, _, err = exchanger.Exchange(context.Background(), srv.URL, form)
	if !errors.Is(err, errOAuthResponseTooLarge) || !cascade.HasKind(err, cascade.KindIntegrity) || body != nil {
		t.Fatalf("a body of cap+1: len %d, err %v; want the size refusal", len(body), err)
	}
	if hits.Load() != 2 {
		t.Fatalf("token server hits %d, want 2", hits.Load())
	}
}

func TestOAuthCallbackRefusesNearMissPaths_Integration(t *testing.T) {
	if u, err := url.Parse(testOAuthConfig().RedirectURI); err != nil || u.Path != "/callback" {
		t.Fatalf("the configured redirect URI %q must have the path /callback", testOAuthConfig().RedirectURI)
	}
	idp := newRealIDP(t)
	var strays []int
	broker, _, _ := realBroker(t, idp, func(ctx context.Context, redirect, state string) error {
		for _, near := range []string{redirect + "/x", redirect + "x"} {
			status, err := browserGet(ctx, http.MethodGet, near+"?code=stray-code")
			if err != nil {
				return err
			}
			strays = append(strays, status)
		}
		return honestBrowser(ctx, redirect, state)
	})
	if _, err := broker.Start(context.Background()); err != nil {
		t.Fatalf("a near-miss path took the callback slot: %v", err)
	}
	if len(strays) != 2 || strays[0] != http.StatusNotFound || strays[1] != http.StatusNotFound {
		t.Fatalf("near-miss GETs answered %v, want [404 404]", strays)
	}
	if idp.count("/token") != 1 {
		t.Fatalf("token endpoint hits %d, want 1", idp.count("/token"))
	}
}
