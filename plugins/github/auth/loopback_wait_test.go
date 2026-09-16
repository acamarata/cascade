package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the callback wait's decision half — the bound, the
//   state check, and what each outcome returns — over a plain channel.
// Constraints: net/url is fine; Art.7.2 bans only "net" and "net/http".
//   The socket half (Listen, handle, RedirectURI) is covered by
//   loopback_integration_test.go behind the `integration` tag.
// SPORT: plugins/github/auth tests (ADD) — P1-E25-W5-S51-T1.

// TestAwaitCallbackAcceptsAMatchingState proves a genuine redirect yields
// its code.
func TestAwaitCallbackAcceptsAMatchingState(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan url.Values, 1)
	incoming <- url.Values{"code": {"the-code"}, "state": {pkce.State}}

	got, err := AwaitCallback(context.Background(), incoming, pkce, time.Minute)
	if err != nil {
		t.Fatalf("AwaitCallback: %v", err)
	}
	if got != "the-code" {
		t.Errorf("code = %q, want the one the redirect carried", got)
	}
}

// TestAwaitCallbackRejectsAForgedState is the security rule: anything that
// can reach the loopback port must not be able to authorize this flow.
func TestAwaitCallbackRejectsAForgedState(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan url.Values, 1)
	incoming <- url.Values{"code": {"the-code"}, "state": {"forged"}}

	if _, err := AwaitCallback(context.Background(), incoming, pkce, time.Minute); err == nil {
		t.Fatal("a callback carrying the wrong state was accepted")
	}
}

// TestAwaitCallbackTimesOut proves an abandoned consent screen ends the
// wait as a typed timeout rather than hanging for the life of the process.
func TestAwaitCallbackTimesOut(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is ever sent on this channel.
	incoming := make(chan url.Values)

	_, err = AwaitCallback(context.Background(), incoming, pkce, time.Millisecond)
	if err == nil {
		t.Fatal("the wait returned success with no callback")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindTimeout {
		t.Errorf("kind = %v (ok=%v), want KindTimeout", kind, ok)
	}
}

// TestAwaitCallbackHonoursItsCallersContext proves a shutdown ends the
// wait without waiting out the full five minutes.
func TestAwaitCallbackHonoursItsCallersContext(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := AwaitCallback(ctx, make(chan url.Values), pkce, time.Hour); err == nil {
		t.Fatal("a cancelled wait returned success")
	}
}

// TestAwaitCallbackReportsAServerRefusal proves an authorization server
// that declined is surfaced as its own refusal, not as a missing code.
func TestAwaitCallbackReportsAServerRefusal(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan url.Values, 1)
	incoming <- url.Values{"error": {"access_denied"}, "state": {pkce.State}}

	if _, err := AwaitCallback(context.Background(), incoming, pkce, time.Minute); err == nil {
		t.Fatal("a declined authorization was accepted")
	}
}

// TestTheFirstRedirectWins pins the rule the handler delegates: a second
// redirect is dropped rather than blocking the handler or overwriting the
// flow the first one already decided.
func TestTheFirstRedirectWins(t *testing.T) {
	incoming := make(chan url.Values, 1)

	if !RecordCallback(incoming, url.Values{"code": {"first"}}) {
		t.Fatal("the first redirect was not recorded")
	}
	if RecordCallback(incoming, url.Values{"code": {"second"}}) {
		t.Fatal("a second redirect displaced the first")
	}

	got := <-incoming
	if got.Get("code") != "first" {
		t.Errorf("recorded code = %q, want the first redirect's", got.Get("code"))
	}
}

// TestTheCallbackPageSaysNothingAboutTheOutcome is the security-relevant
// half of the handler: the browser is told the response arrived, never
// whether it authorized anything.
func TestTheCallbackPageSaysNothingAboutTheOutcome(t *testing.T) {
	for _, leak := range []string{"success", "failed", "error", "denied", "token"} {
		if strings.Contains(strings.ToLower(CallbackPageBody), leak) {
			t.Errorf("the callback page mentions %q; it must not report the outcome", leak)
		}
	}
}
