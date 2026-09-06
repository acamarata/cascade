package client

// Purpose: ContextScopeShow's own unit tests, at the encodeRequest/
//   decodeResponse level -- no "net"/"net/http" import (Art.7.2's default
//   no-network unit lane); the real socket round trip is exercised by
//   internal/integration/context_scope_test.go's Article-2 fixture.
// SPORT: internal/client (ADD, per T-4 sport_updates).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
)

func TestContextScopeMethodLiteral(t *testing.T) {
	if daemon.ContextScopeMethod != "context.scope.show" {
		t.Errorf("ContextScopeMethod = %q, want %q", daemon.ContextScopeMethod, "context.scope.show")
	}
}

// TestContextScopeShowEncodesParams proves the request-side half of the
// wire contract: ContextScopeShow's params marshal to exactly
// scope.ShowParams's JSON shape, with the method name this package's
// daemon.ContextScopeMethod names.
func TestContextScopeShowEncodesParams(t *testing.T) {
	params := scope.ShowParams{Branch: "main", Session: "s1"}
	body, err := encodeRequest(daemon.ContextScopeMethod, params, "req-1")
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"method":"context.scope.show"`) {
		t.Errorf("encoded request missing method: %s", s)
	}
	if !strings.Contains(s, `"branch":"main"`) || !strings.Contains(s, `"session":"s1"`) {
		t.Errorf("encoded request missing params fields: %s", s)
	}
}

// TestContextScopeShowDecodesResult proves the response-side half: a
// context.scope.show result decodes into scope.SessionScope with every
// field intact.
func TestContextScopeShowDecodesResult(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","result":{"kind":"general","user":"u1","session":"s1"}}`)
	var got scope.SessionScope
	if err := decodeResponse(daemon.ContextScopeMethod, "req-1", body, &got); err != nil {
		t.Fatalf("decodeResponse: %v", err)
	}
	if got.Kind != scope.ScopeKindGeneral || got.User != "u1" || got.Session != "s1" {
		t.Errorf("decoded SessionScope = %+v, want kind=general user=u1 session=s1", got)
	}
}

func TestContextScopeShowDecodesRPCError(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","error":{"code":-32602,"message":"invalid params","data":{"kind":"invalid-input"}}}`)
	var got scope.SessionScope
	err := decodeResponse(daemon.ContextScopeMethod, "req-1", body, &got)
	if err == nil {
		t.Fatal("decodeResponse over an RPC error = nil, want error")
	}
}

// TestScopeShowParamsRoundTrip proves ShowParams itself round-trips
// through JSON with every field, since ContextScopeShow's wire contract
// depends on that shape staying stable.
func TestScopeShowParamsRoundTrip(t *testing.T) {
	want := scope.ShowParams{
		Cwd: "/x", User: "u", Machine: "m", Branch: "b", Task: "t", Session: "s", ExplicitOverrides: "eo",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got scope.ShowParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
