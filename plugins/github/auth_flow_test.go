package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// Purpose (this file): the OAuth flow end to end — begin, callback,
//   exchange, vault storage — driven through injected seams so no socket is
//   bound and no token endpoint is called.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// beginFlow runs cascade.auth.begin and returns the plugin, its output
// stream, and the reply's result map.
func beginFlow(t *testing.T, p *broker) map[string]string {
	t.Helper()
	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":"Iv1.test"}`)})
	if !reply || out.Error != nil {
		t.Fatalf("auth.begin returned %+v", out)
	}
	result, ok := out.Result.(map[string]string)
	if !ok {
		t.Fatalf("result = %T, want map[string]string", out.Result)
	}
	return result
}

// TestAuthBeginRefusesUnderNoInput proves the no-input rule reaches the
// wire: the refusal must come back as an error frame, not as an
// authorization URL nobody can open.
func TestAuthBeginRefusesUnderNoInput(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "1")
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, nil)

	id := uint64(3)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.begin",
		Params: json.RawMessage(`{"client_id":"c"}`)})
	if !reply || out.Error == nil {
		t.Fatalf("auth.begin under CASCADE_NO_INPUT=1 returned %+v, want an error", out)
	}
	if !strings.Contains(out.Error.Message, "CASCADE_NO_INPUT") {
		t.Errorf("error = %q, want it to name the variable", out.Error.Message)
	}
}

// TestAuthBeginNeverReturnsTheVerifier is the secret-handling assertion:
// the PKCE verifier is the half that proves this process started the flow
// and must never leave it.
func TestAuthBeginNeverReturnsTheVerifier(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, nil)
	result := beginFlow(t, p)

	if _, present := result["verifier"]; present {
		t.Fatal("auth.begin returned the PKCE verifier")
	}
	if result["authorize_url"] == "" || result["state"] == "" {
		t.Fatalf("auth.begin returned %v, want an authorize_url and a state", result)
	}
	for _, field := range []string{"authorize_url", "state", "redirect_uri", "scope"} {
		if strings.Contains(result[field], p.flows.pending.pkce.Verifier) {
			t.Errorf("%s carries the PKCE verifier", field)
		}
	}
}

// TestAuthCompleteArmsTheClientAndNeverClaimsStored asserts on STATE: the
// next API call carries the token, and the reply does not claim a store.
func TestAuthCompleteArmsTheClientAndNeverClaimsStored(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{body: `{"access_token":"gho_live","token_type":"bearer","scope":"repo"}`}
	doer := &fakeDoer{body: []byte(`[]`)}
	p, out := newTestPlugin(doer, waiter, poster)

	result := beginFlow(t, p)
	waiter.query = url.Values{"code": {"the-code"}, "state": {result["state"]}}

	id := uint64(2)
	reply, ok := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if !ok || reply.Error != nil {
		t.Fatalf("auth.complete returned %+v", reply)
	}
	if out.Len() != 0 {
		t.Errorf("auth.complete wrote to the stream: %q", out.String())
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "gho_live") {
		t.Errorf("the reply frame carries the token: %s", encoded)
	}

	// The exchange proved the flow with the verifier, and never sent a
	// client secret — there is none to send for a public client.
	if poster.form.Get("code_verifier") == "" {
		t.Error("the exchange sent no code_verifier; PKCE was not proven")
	}
	if poster.form.Get("client_secret") != "" {
		t.Error("the exchange sent a client_secret; this is a public client")
	}

	// STATE, not an event: the next API call must actually carry the token.
	id = uint64(3)
	if _, ok := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".repos.list",
		Params: json.RawMessage(`{"owner":"acamarata"}`)}); !ok {
		t.Fatal("repos.list produced no reply")
	}
	if doer.token != "gho_live" {
		t.Errorf("the API call carried token %q, want the one just obtained", doer.token)
	}
	if !waiter.closed {
		t.Error("the callback listener was left open after the flow completed")
	}
}

// TestAuthCompleteRejectsAForgedCallback is the state check. Without it a
// callback delivered by anything that can reach the loopback port would
// authorize a flow this process never started.
func TestAuthCompleteRejectsAForgedCallback(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{body: `{"access_token":"gho_live","token_type":"bearer"}`}
	p, out := newTestPlugin(&fakeDoer{}, waiter, poster)

	beginFlow(t, p)
	waiter.query = url.Values{"code": {"the-code"}, "state": {"not-the-state"}}

	id := uint64(2)
	reply, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if reply.Error == nil {
		t.Fatal("a callback carrying the wrong state was accepted")
	}
	if out.Len() != 0 {
		t.Error("the stream was written to despite the state mismatch")
	}
	if poster.form != nil {
		t.Error("the code was sent to the token endpoint despite the state mismatch")
	}
}

// TestAuthCompleteWithoutABeginIsRefused proves completion cannot run
// against a flow that was never started.
func TestAuthCompleteWithoutABeginIsRefused(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, &fakePoster{})
	id := uint64(1)
	reply, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if reply.Error == nil {
		t.Fatal("auth.complete succeeded with no authorization in progress")
	}
	if !strings.Contains(reply.Error.Message, "begin") {
		t.Errorf("error = %q, want it to say what to call first", reply.Error.Message)
	}
}

// TestASecondBeginClosesTheFirstListener proves a restarted flow does not
// leave a socket bound for the life of the process.
func TestASecondBeginClosesTheFirstListener(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	first, second := &fakeWaiter{}, &fakeWaiter{}
	waiters := []*fakeWaiter{first, second}
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, nil)
	p.flows.listen = func() (callbackWaiter, error) {
		w := waiters[0]
		waiters = waiters[1:]
		return w, nil
	}

	beginFlow(t, p)
	beginFlow(t, p)

	if !first.closed {
		t.Error("beginning a second flow left the first listener bound")
	}
	if second.closed {
		t.Error("the second flow's listener was closed while it is still pending")
	}
}

// TestTokenEndpointFailureIsNotStored proves a refused exchange arms
// nothing and writes nothing.
func TestTokenEndpointFailureIsNotStored(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{status: 401, body: `{"error":"bad_verification_code"}`}
	p, out := newTestPlugin(&fakeDoer{}, waiter, poster)

	result := beginFlow(t, p)
	waiter.query = url.Values{"code": {"the-code"}, "state": {result["state"]}}

	id := uint64(2)
	reply, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.auth.complete"})
	if reply.Error == nil {
		t.Fatal("a refused exchange reported success")
	}
	if out.Len() != 0 {
		t.Error("the stream was written to after the exchange was refused")
	}
}

// completeFlow runs begin and complete against a token endpoint that
// returns token, returning the reply and every byte the plugin wrote.
func completeFlow(t *testing.T, token string) (frame, string) {
	t.Helper()
	t.Setenv("CASCADE_NO_INPUT", "")
	waiter := &fakeWaiter{}
	poster := &fakePoster{body: `{"access_token":"` + token + `","token_type":"bearer","scope":"repo"}`}
	p, _ := newTestPlugin(&fakeDoer{}, waiter, poster)
	begin := beginFlow(t, p)
	waiter.query = url.Values{"code": {"the-code"}, "state": {begin["state"]}}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"cascade.auth.complete"}` + "\n")
	var stdout, stderr strings.Builder
	if err := p.run(in, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	var reply frame
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &reply); err != nil {
		t.Fatalf("the stream is not one reply frame: %q (%v)", stdout.String(), err)
	}
	return reply, stdout.String() + stderr.String()
}

// TestAuthCompleteNeverClaimsStored: no host stores a plugin secret in P1,
// so the reply says stored:false and names the deferral and the env path.
func TestAuthCompleteNeverClaimsStored(t *testing.T) {
	reply, _ := completeFlow(t, "gho_claimcheck")
	result, ok := reply.Result.(map[string]any)
	if reply.Error != nil || !ok {
		t.Fatalf("auth.complete reply = %+v", reply)
	}
	if result["stored"] != false {
		t.Fatalf("stored = %v, want false", result["stored"])
	}
	reason, _ := result["reason"].(string)
	if !strings.Contains(reason, "DEF-P2-plugin-secret-store") || !strings.Contains(reason, "CASCADE_GITHUB_TOKEN") {
		t.Fatalf("reason = %q, want the deferral id and the environment-token path", reason)
	}
	if _, has := result["key"]; has {
		t.Errorf("the reply carries a stored-key member: %v", result)
	}
}

// TestPluginNeverSendsTokenValue: a canary token obtained through the auth
// flow appears in no frame the plugin writes to stdout or stderr.
func TestPluginNeverSendsTokenValue(t *testing.T) {
	const canary = "gho_CANARY_never_on_the_wire_0451"
	reply, wire := completeFlow(t, canary)
	if reply.Error != nil {
		t.Fatalf("auth.complete: %+v", reply)
	}
	if strings.Contains(wire, canary) || strings.Contains(wire, "host_secret_ref") {
		t.Fatalf("the plugin wrote the token or a host_secret_ref frame: %q", wire)
	}
}
