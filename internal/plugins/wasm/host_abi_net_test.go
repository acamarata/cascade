package wasm

import (
	"context"
	"testing"
)

// TestHostHTTP_BlockedOutsideDeclaredNetScope proves host_http refuses a
// call whose target host is outside the plugin's declared net scope
// with a typed error, and — the load-bearing part — never invokes
// Deps.Net at all (no network call is attempted).
func TestHostHTTP_BlockedOutsideDeclaredNetScope(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "net-blocked", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	net := &fakeNet{}
	deps.Net = net

	var resp HTTPResponse
	err = rt.Dispatch(ctx, lm, "call_host_http", "p", []string{"allowed.example.com"}, deps, WASIConfig{}, testLimits(),
		HTTPRequest{Method: "GET", URL: "https://not-allowed.example.com/x"}, &resp)
	if err == nil {
		t.Fatal("expected a net-scope-violation error")
	}
	if net.called {
		t.Fatal("Deps.Net.Do was called despite the scope violation")
	}
}

// TestHostHTTP_AllowedWithinScope proves the mirror case: an in-scope
// URL is dispatched through to Deps.Net.
func TestHostHTTP_AllowedWithinScope(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "net-allowed", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	net := &fakeNet{resp: HTTPResponse{Status: 200, Body: []byte("ok")}}
	deps.Net = net

	var resp HTTPResponse
	err = rt.Dispatch(ctx, lm, "call_host_http", "p", []string{"*.example.com"}, deps, WASIConfig{}, testLimits(),
		HTTPRequest{Method: "GET", URL: "https://api.example.com/x"}, &resp)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !net.called || resp.Status != 200 {
		t.Fatalf("called=%v resp=%+v", net.called, resp)
	}
}

func TestHostHTTP_EmptyScopeRefusesEverything(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "net-empty", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	net := &fakeNet{}
	deps.Net = net

	var resp HTTPResponse
	err = rt.Dispatch(ctx, lm, "call_host_http", "p", nil, deps, WASIConfig{}, testLimits(),
		HTTPRequest{Method: "GET", URL: "https://anything.example.com/"}, &resp)
	if err == nil || net.called {
		t.Fatalf("expected refusal with no scopes; err=%v called=%v", err, net.called)
	}
}

func TestHostHTTP_MalformedURLRefused(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "net-malformed", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	net := &fakeNet{}
	deps.Net = net

	var resp HTTPResponse
	err = rt.Dispatch(ctx, lm, "call_host_http", "p", []string{"example.com"}, deps, WASIConfig{}, testLimits(),
		HTTPRequest{Method: "GET", URL: "://not a url"}, &resp)
	if err == nil || net.called {
		t.Fatalf("expected refusal for a malformed URL; err=%v called=%v", err, net.called)
	}
}

// TestScopeMatches_EmptyScopeEntry proves an empty-string scope entry in
// the plugin's declared scopes list matches nothing (fails closed rather
// than being treated as a wildcard or a no-op match).
func TestScopeMatches_EmptyScopeEntry(t *testing.T) {
	if scopeMatches("", "example.com") {
		t.Fatal("an empty scope entry must never match any host")
	}
}

func TestCheckNetScope_Table(t *testing.T) {
	cases := []struct {
		url    string
		scopes []string
		ok     bool
	}{
		{"https://example.com/x", []string{"example.com"}, true},
		{"https://sub.example.com/x", []string{"*.example.com"}, true},
		{"https://example.com/x", []string{"*.example.com"}, false}, // wildcard excludes bare apex.
		{"https://evil.com/x", []string{"example.com"}, false},
		{"not a url", []string{"example.com"}, false},
		{"https://example.com/x", nil, false},
	}
	for _, c := range cases {
		err := checkNetScope(c.url, c.scopes)
		if (err == nil) != c.ok {
			t.Errorf("checkNetScope(%q, %v) err=%v, want ok=%v", c.url, c.scopes, err, c.ok)
		}
	}
}
