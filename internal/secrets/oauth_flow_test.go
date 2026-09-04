// Purpose: the OAuth broker's refusal paths - CSRF state, the
//
//	CASCADE_NO_INPUT guard, the authorization deadline, and the canary
//	sweep that proves no token reaches any output.
//
// Constraints: same no-network rule as oauth_test.go.
// SPORT: OAUTH_BROKER: ADD (tests).

package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestOAuthCallbackWithUnknownStateIsRefused(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state=an-unsolicited-state")
	_, err := h.broker.Start(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("an unsolicited callback was accepted: %v", err)
	}
	if len(h.custody.entries) != 0 {
		t.Fatalf("a refused callback still wrote to the vault: %v", h.custody.entries)
	}
	if h.idp.callCount() != 0 {
		t.Fatal("a refused callback still reached the token endpoint")
	}
}

func TestOAuthCallbackWithReusedStateIsRefused(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	if _, err := h.broker.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Replay the exact query the first authorization consumed. consumeState
	// removed that state as it read it, so the replay finds nothing.
	stale := h.lastQuery
	h.listener.resolve = func() string { return stale }
	_, err := h.broker.Start(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("a replayed state was accepted: %v", err)
	}
}

func TestOAuthCallbackWithExpiredStateIsRefused(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	// The listener answers, but the clock has moved past the window by the
	// time the state is consumed.
	h.broker.clock = &advancingClock{at: h.now, step: 2 * defaultAuthDeadline}
	_, err := h.broker.Start(context.Background())
	if !errors.Is(err, ErrStateExpired) {
		t.Fatalf("an expired state was accepted: %v", err)
	}
}

// advancingClock returns at, then at+step for every later call. It models
// "time passed while the operator was in the browser" without a sleep.
type advancingClock struct {
	at   time.Time
	step time.Duration
	n    int
}

func (c *advancingClock) Now() time.Time {
	c.n++
	if c.n == 1 {
		return c.at
	}
	return c.at.Add(c.step)
}

func TestOAuthNoInputRefusesBeforeBindingOrSpawning(t *testing.T) {
	vault, _ := newTestBroker(t, &allowGate{})
	listenCalls := 0
	broker, err := newOAuthBroker(testOAuthConfig(), OAuthDeps{
		Vault: vault, Clock: fixedClock{at: time.Unix(1, 0)},
		LookupEnv: func(key string) (string, bool) {
			if key == noInputEnv {
				return "1", true
			}
			return "", false
		},
	}, &mockIDP{}, func(context.Context) (callbackListener, error) {
		listenCalls++
		return &stubListener{port: "1"}, nil
	}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatalf("newOAuthBroker: %v", err)
	}
	before := runtime.NumGoroutine()
	_, startErr := broker.Start(context.Background())
	if !errors.Is(startErr, ErrNoInput) {
		t.Fatalf("CASCADE_NO_INPUT=1 did not refuse: %v", startErr)
	}
	if after := runtime.NumGoroutine(); after != before {
		t.Fatalf("goroutine count moved from %d to %d; the refusal spawned work", before, after)
	}
	if listenCalls != 0 {
		t.Fatalf("the refusal still bound a listener (%d calls)", listenCalls)
	}
}

func TestOAuthDeadlineTimesOutAndClosesTheListener(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "")
	h.listener.block = true
	// A short real timeout rather than a sleep: the no-sleep gate forbids
	// time.Sleep, and the wait is bounded by a context timeout anyway.
	h.broker.deadline = 10 * time.Millisecond
	_, err := h.broker.Start(context.Background())
	if !errors.Is(err, ErrCallbackTimeout) {
		t.Fatalf("the deadline did not produce a timeout refusal: %v", err)
	}
	if h.listener.closes == 0 {
		t.Fatal("the listener was not closed after the deadline")
	}
}

func TestOAuthCancellationClosesTheListener(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "")
	h.listener.block = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.broker.Start(ctx); err == nil {
		t.Fatal("a canceled authorization returned no error")
	}
	if h.listener.closes == 0 {
		t.Fatal("the listener was not closed after cancellation")
	}
}

// oauthCanaries is every credential string the flow handles.
func oauthCanaries() []string { return []string{canaryAccess, canaryRefresh, canaryCode} }

// assertNoCanary fails when any canary appears in text.
func assertNoCanary(t *testing.T, where, text string) {
	t.Helper()
	for _, canary := range oauthCanaries() {
		if strings.Contains(text, canary) {
			t.Fatalf("%s leaked a credential (%s)", where, canary)
		}
	}
}

func TestOAuthNoTokenReachesAnyOutput(t *testing.T) {
	cfg := testOAuthConfig()
	cfg.NoBrowser = true
	h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertNoCanary(t, "the diagnostics stream", h.diag.String())
	assertNoCanary(t, "the record's fmt output", fmt.Sprintf("%v %+v %#v", rec, rec, rec))
	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling the record: %v", err)
	}
	assertNoCanary(t, "the record's JSON", string(encoded))
	names, err := h.vault.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertNoCanary(t, "vault list output", strings.Join(names, " "))
	assertNoCanary(t, "the broker's fmt output", fmt.Sprintf("%v", h.broker.store))
}

func TestOAuthNoTokenReachesAFailurePath(t *testing.T) {
	cases := map[string]func(*oauthHarness){
		"exchange refuses":        func(h *oauthHarness) { h.idp.body, h.idp.status = `{"error":"access_denied"}`, 400 },
		"exchange is unreachable": func(h *oauthHarness) { h.idp.failWith = errors.New("dial tcp 127.0.0.1:443: connection refused") },
		"response is not JSON":    func(h *oauthHarness) { h.idp.body, h.idp.status = canaryAccess, 200 },
		"vault write fails": func(h *oauthHarness) {
			h.custody.failOn, h.custody.err = "set", errors.New("read-only store")
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testOAuthConfig()
			cfg.NoBrowser = true
			h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
			arrange(h)
			_, err := h.broker.Start(context.Background())
			if err == nil {
				t.Fatal("the arranged failure produced no error")
			}
			assertNoCanary(t, "the error message", err.Error())
			assertNoCanary(t, "the diagnostics stream", h.diag.String())
			assertNoCanary(t, "the error's verbose formatting", fmt.Sprintf("%+v", err))
		})
	}
}

func TestOAuthNoBrowserPrintsTheURLAndKeepsWaiting(t *testing.T) {
	cfg := testOAuthConfig()
	cfg.NoBrowser = true
	h := newOAuthHarness(t, cfg, "code="+canaryCode+"&state={{state}}")
	if _, err := h.broker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.opened) != 0 {
		t.Fatalf("NoBrowser still launched a browser: %v", h.opened)
	}
	if !strings.Contains(h.diag.String(), "code_challenge=") {
		t.Fatalf("the authorization URL was not printed: %q", h.diag.String())
	}
}

func TestOAuthBrowserFailureFallsBackToPrintingTheURL(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	inner := h.broker.open
	h.broker.open = func(ctx context.Context, rawURL string) error {
		_ = inner(ctx, rawURL)
		return errors.New("no browser on this host")
	}
	if _, err := h.broker.Start(context.Background()); err != nil {
		t.Fatalf("a failed browser launch aborted the flow: %v", err)
	}
	if !strings.Contains(h.diag.String(), "code_challenge=") {
		t.Fatal("the fallback did not print the authorization URL")
	}
}

func TestOAuthBrokerRefusesIncompleteWiring(t *testing.T) {
	vault, _ := newTestBroker(t, &allowGate{})
	clock := fixedClock{at: time.Unix(1, 0)}
	listen := func(context.Context) (callbackListener, error) { return &stubListener{}, nil }
	open := func(context.Context, string) error { return nil }
	if _, err := newOAuthBroker(testOAuthConfig(), OAuthDeps{Clock: clock}, &mockIDP{}, listen, open); err == nil {
		t.Fatal("a broker with no vault was accepted")
	}
	if _, err := newOAuthBroker(testOAuthConfig(), OAuthDeps{Vault: vault}, &mockIDP{}, listen, open); err == nil {
		t.Fatal("a broker with no clock was accepted")
	}
	if _, err := newOAuthBroker(testOAuthConfig(), OAuthDeps{Vault: vault, Clock: clock}, nil, listen, open); err == nil {
		t.Fatal("a broker with no exchanger was accepted")
	}
	bad := testOAuthConfig()
	bad.PKCEMethod = "plain"
	if _, err := newOAuthBroker(bad, OAuthDeps{Vault: vault, Clock: clock}, &mockIDP{}, listen, open); err == nil {
		t.Fatal("the plain PKCE method was accepted")
	}
}

func TestOAuthDefaultsAreApplied(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	if h.broker.deadline != defaultAuthDeadline {
		t.Fatalf("deadline %v, want the 5-minute default", h.broker.deadline)
	}
	if h.broker.skew != defaultExpirySkew {
		t.Fatalf("skew %v, want the default", h.broker.skew)
	}
	if h.broker.rand == nil {
		t.Fatal("no entropy source was defaulted in")
	}
	var _ provider.OAuthBroker = h.broker
}
