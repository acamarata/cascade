package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/github/auth"
)

// Purpose (this file): the auth flow's FAILURE branches — the ones a real
//   install meets and the happy-path tests never reach: a listener that
//   cannot bind, a stream that cannot be written to, and a client id the
//   config rejects.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// TestBeginReportsAListenerThatCannotBind proves a bind failure is a typed
// refusal rather than a flow that returns an authorization URL pointing at
// a port nothing is listening on — which would look like success and then
// hang until the callback timeout.
func TestBeginReportsAListenerThatCannotBind(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	p.flows.listen = func() (callbackWaiter, error) {
		return nil, errors.New("bind: address already in use")
	}

	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":"Iv1.test"}`)})
	if !reply || out.Error == nil {
		t.Fatalf("a failed bind returned %+v, want an error", out)
	}
	if !strings.Contains(out.Error.Message, "address already in use") {
		t.Errorf("error = %q, want the bind failure", out.Error.Message)
	}
	if p.flows.pending != nil {
		t.Error("a flow was left pending after the listener failed to bind")
	}
}

// TestBeginRefusesAnUnusableClientID proves the config refusal reaches the
// wire rather than producing an authorization URL with no client on it.
func TestBeginRefusesAnUnusableClientID(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	p, _ := newTestPlugin(&fakeDoer{}, waiter, nil)

	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":"   "}`)})
	if !reply || out.Error == nil {
		t.Fatalf("a blank client id returned %+v, want an error", out)
	}
	if waiter.closed {
		t.Error("a listener was bound and closed for a flow that never started")
	}
}

// brokenStream fails every write, standing in for a host that closed the
// pipe while this plugin was still answering.
type brokenStream struct{}

func (brokenStream) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

// TestCompleteReportsAStreamItCannotWriteTo is the failure that matters
// most on this path: the token was obtained, but the notification carrying
// it to the vault could not be written. That must surface as an error — a
// caller told "stored" when nothing was stored would never retry.
func TestCompleteReportsAStreamItCannotWriteTo(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{body: `{"access_token":"gho_live","token_type":"bearer"}`}
	p, _ := newTestPlugin(&fakeDoer{}, waiter, poster)
	result := beginFlow(t, p)
	waiter.query = url.Values{"code": {"the-code"}, "state": {result["state"]}}

	// The host's pipe goes away between begin and complete.
	p.out = brokenStream{}

	id := uint64(2)
	out, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if out.Error == nil {
		t.Fatal("a token that could not be handed to the vault was reported as stored")
	}
	if !strings.Contains(out.Error.Message, "pipe closed") {
		t.Errorf("error = %q, want the write failure", out.Error.Message)
	}
}

// TestEmitRefusesWithNoStream proves a broker with nowhere to write fails
// as a typed error rather than a nil-pointer panic mid-flow.
func TestEmitRefusesWithNoStream(t *testing.T) {
	if err := (&broker{}).emit([]byte(`{}`)); err == nil {
		t.Fatal("emit succeeded with no output stream")
	}
}

// TestTheDefaultListenerIsTheRealOne proves an unconfigured flowState
// reaches the production loopback constructor rather than silently doing
// nothing — the seam exists for tests, and its ABSENCE must mean
// production behaviour.
func TestTheDefaultListenerIsTheRealOne(t *testing.T) {
	if (&flowState{}).listener() == nil {
		t.Fatal("an unconfigured flowState has no listener factory")
	}
}

// TestMalformedBeginParamsAreRefused covers the params decode branch.
func TestMalformedBeginParamsAreRefused(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, nil)
	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":{"not":"a string"}}`)})
	if !reply || out.Error == nil {
		t.Fatalf("malformed begin params returned %+v, want an error", out)
	}
}

// TestStoreTokenNotificationRoundTrips proves the frame decodes back to
// exactly the key and value it was given, rather than only that it
// contains them somewhere.
func TestStoreTokenNotificationRoundTrips(t *testing.T) {
	raw, err := StoreTokenNotification(TokenKey(), "gho_secret")
	if err != nil {
		t.Fatalf("StoreTokenNotification: %v", err)
	}
	var decoded struct {
		Method string            `json:"method"`
		Params map[string]string `json:"params"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the frame is not JSON: %v", err)
	}
	if decoded.Params["key"] != TokenKey() || decoded.Params["value"] != "gho_secret" {
		t.Fatalf("params = %v", decoded.Params)
	}
}

// TestCompleteRefusesAnUnusableClientIDAfterTheCallback covers the config
// branch on the completion side: the flow began with an id that Config
// later rejects, so the exchange must not be attempted.
func TestCompleteRefusesAnUnusableClientIDAfterTheCallback(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{body: `{"access_token":"x","token_type":"bearer"}`}
	p, _ := newTestPlugin(&fakeDoer{}, waiter, poster)

	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	// A pending flow whose client id Config refuses.
	p.flows.replace(&pendingFlow{pkce: pkce, loopback: waiter, clientID: "   "})
	waiter.query = url.Values{"code": {"the-code"}, "state": {pkce.State}}

	id := uint64(2)
	out, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if out.Error == nil {
		t.Fatal("a flow with an unusable client id completed")
	}
	if poster.form != nil {
		t.Error("the code was sent to the token endpoint despite the config refusal")
	}
}

// addresslessWaiter is a listener that came back without an address — the
// shape a half-initialised transport would have.
type addresslessWaiter struct{ fakeWaiter }

func (addresslessWaiter) RedirectURI() string { return "" }

// TestAListenerWithNoAddressIsClosedNotLeaked is the resource assertion on
// begin's last failure branch. The listener is already bound at that point,
// so returning the error without closing it would hold a socket for the
// life of the process with no flow able to use it.
func TestAListenerWithNoAddressIsClosedNotLeaked(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &addresslessWaiter{}
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	p.flows.listen = func() (callbackWaiter, error) { return waiter, nil }

	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":"Iv1.test"}`)})
	if !reply || out.Error == nil {
		t.Fatalf("a listener with no address returned %+v, want an error", out)
	}
	if !waiter.closed {
		t.Error("the bound listener was leaked when the authorize URL could not be built")
	}
	if p.flows.pending != nil {
		t.Error("a flow was left pending after the authorize URL failed")
	}
}

// TestBlankLinesBetweenFramesAreSkipped pins the framing tolerance. A host
// that writes a trailing newline must not have it reported as an
// undecodable frame on stderr, and must not stall the loop.
func TestBlankLinesBetweenFramesAreSkipped(t *testing.T) {
	p := &broker{doer: &fakeDoer{}}
	var stdout, stderr strings.Builder

	stdin := strings.NewReader("\n" +
		`{"jsonrpc":"2.0","id":1,"method":"cascade.hello"}` + "\n" +
		"\n" +
		`{"jsonrpc":"2.0","id":2,"method":"cascade.hello"}` + "\n")
	if err := p.run(stdin, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Count(strings.TrimSpace(stdout.String()), "\n") + 1; got != 2 {
		t.Errorf("the host received %d replies, want 2", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("a blank line was reported as a bad frame: %q", stderr.String())
	}
}
