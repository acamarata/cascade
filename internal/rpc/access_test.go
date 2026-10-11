package rpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = DeclareAccess("test.principal.scoped", AccessScoped)
var _ = DeclareAccess("test.principal.view", AccessOwnerView)
var _ = DeclareAccess("test.principal.unknown", Access(accessMax+1))
var _ = DeclareAccess("test.principal.max", Access(255))

func principalFixture(t *testing.T) (*principal.Minter, *runtime.FixedClock, string) {
	t.Helper()
	c := runtime.NewFixedClock(time.Unix(100, 0))
	m := principal.NewMinter(c)
	token, err := m.Mint(principal.Principal{ID: "test-caller", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return m, c, token
}

func admittedRequest(h *Handler, method, path, token, marker string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://unix"+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test.principal.scoped"}`))
	r = r.WithContext(ctxWithPeerCred(ownerUID(), true))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set(principal.HeaderToken, token)
	}
	if marker != "" {
		r.Header.Set(principal.HeaderOwnerCLI, marker)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestScopedVerbRefusesAnonymous(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	n := 0
	for name, a := range methodAccess {
		if a != AccessScoped && a != AccessOwnerView && a != AccessOwnerOnly {
			continue
		}
		n++
		r.Register(name, func(context.Context, json.RawMessage) (any, error) { t.Error("handler reached"); return nil, nil })
		_, err := r.Dispatch(ctxWithPeerCred(ownerUID(), true), &Request{Method: name})
		if err == nil || err.Code != cascade.RPCCodePermissionDenied {
			t.Errorf("%s anonymous: %v", name, err)
		}
		if a == AccessScoped {
			_, err = r.Dispatch(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), &Request{Method: name})
			if err == nil || err.Code != cascade.RPCCodePermissionDenied {
				t.Errorf("%s owner: %v", name, err)
			}
		}
	}
	if n < 2 {
		t.Fatal("empty access iteration")
	}
}

func TestPrincipalRefusedOnOwnerOnlyVerbs(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	p := principal.Principal{ID: "test", Scope: "project:test"}
	for _, method := range []string{"vault.get", "config.get", "policy.set", "approval.grant", "elevation.set_allow_remote", "node.enroll"} {
		calls := 0
		r.Register(method, func(context.Context, json.RawMessage) (any, error) {
			calls++
			return nil, cascade.New(cascade.KindInvalidInput, "normal handler check")
		})
		_, err := r.Dispatch(principal.WithCaller(context.Background(), principal.Caller{Principal: &p}), &Request{Method: method})
		if err == nil || err.Code != cascade.RPCCodePermissionDenied || calls != 0 {
			t.Errorf("%s principal: %v calls=%d", method, err, calls)
		}
		_, err = r.Dispatch(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), &Request{Method: method})
		if err == nil || err.Message != "invalid-input: normal handler check" || calls != 1 {
			t.Errorf("%s owner: %v calls=%d", method, err, calls)
		}
	}
}

func TestPrincipalTokenRefusals(t *testing.T) {
	m, clock, expired := principalFixture(t)
	store := storetest.NewMemStore()
	m.Audit = runtime.NewStoreAuditRecorder(store, clock)
	clock.Advance(2 * time.Hour)
	revoked, err := m.Mint(principal.Principal{ID: "revoked", Kind: principal.KindHook, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	m.Revoke("revoked")
	_, _, other := principalFixture(t)
	var first error
	for _, token := range []string{"cpt1_" + strings.Repeat("A", 43), expired, revoked, other} {
		_, err = m.Resolve(token, clock.Now())
		kind, _ := cascade.KindOf(err)
		if err == nil || kind != cascade.KindPermissionDenied || err.(*cascade.Error).Msg != "caller principal not recognized" {
			t.Fatalf("refusal: %v", err)
		}
		if first != nil && first != err {
			t.Fatal("refusal identity differs")
		}
		first = err
	}
	rows, err := store.Scan(context.Background(), "audit", "caller_principal_refused/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	reasons := map[string]int{}
	n := 0
	for rows.Next(context.Background()) {
		n++
		var rec struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(rows.Value(), &rec); err != nil {
			t.Fatal(err)
		}
		reasons[rec.Fields["reason"]]++
		if strings.Contains(string(rows.Value()), "cpt1_") {
			t.Fatal("token in audit")
		}
	}
	if n != 4 || rows.Err() != nil || reasons["restart/unknown"] != 2 || reasons["expired"] != 1 || reasons["revoked"] != 1 {
		t.Fatal(reasons)
	}
}

func TestBothHeadersRefused(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	w := admittedRequest(NewHandler(r), "POST", RPCPath, "cpt1_"+strings.Repeat("B", 43), "owner-cli")
	if w.Code != 400 || !strings.Contains(w.Body.String(), cascade.KindInvalidInput.String()) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestBrowserShapedStillRefusedFirst(t *testing.T) {
	m, clock, token := principalFixture(t)
	lookups := 0
	r := NewRegistry()
	r.SetAccessPolicy(func(token string) (principal.Principal, error) { lookups++; return m.Resolve(token, clock.Now()) })
	h := NewHandlerWithSSE(r, NewSSEMux(nil))
	for _, method := range []string{"POST", "GET"} {
		path := RPCPath
		if method == "GET" {
			path = EventsPath
		}
		req := httptest.NewRequest(method, "http://unix"+path, nil).WithContext(ctxWithPeerCred(ownerUID(), true))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "null")
		req.Header.Set(principal.HeaderToken, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 403 || lookups != 0 {
			t.Fatal(w.Code, lookups)
		}
	}
}

func TestPreRestartTokenRefusedLikeUnknown(t *testing.T) {
	_, _, stale := principalFixture(t)
	m, clock, fresh := principalFixture(t)
	store := storetest.NewMemStore()
	m.Audit = runtime.NewStoreAuditRecorder(store, clock)
	r := NewRegistry()
	r.SetAccessPolicy(func(s string) (principal.Principal, error) { return m.Resolve(s, clock.Now()) })
	calls := 0
	r.Register("test.principal.scoped", func(context.Context, json.RawMessage) (any, error) { calls++; return true, nil })
	h := NewHandler(r)
	var body string
	for _, token := range []string{stale, "cpt1_" + strings.Repeat("C", 43)} {
		w := admittedRequest(h, "POST", RPCPath, token, "")
		if w.Code != 403 || calls != 0 || !strings.Contains(w.Body.String(), "caller principal not recognized") {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
		if body != "" && body != w.Body.String() {
			t.Fatal("restart and forged refusals differ")
		}
		body = w.Body.String()
	}
	rows, err := store.Scan(context.Background(), "audit", "caller_principal_refused/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next(context.Background()) {
		n++
		if !strings.Contains(string(rows.Value()), "restart/unknown") {
			t.Fatal(string(rows.Value()))
		}
	}
	if n != 2 || rows.Err() != nil {
		t.Fatal(n, rows.Err())
	}
	w := admittedRequest(h, "POST", RPCPath, fresh, "")
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), `"result":true`) {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
}

func TestDeclareTopicAccessRejectsOtherClasses(t *testing.T) {
	for _, a := range []Access{AccessScoped, AccessOpen, AccessIngest} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("class %d accepted", a)
				}
			}()
			DeclareTopicAccess("test.invalid", a)
		}()
	}
}

func TestUnknownAccessClassRefused(t *testing.T) {
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	calls, middleware := 0, 0
	r.Use(func(_ string, next HandlerFunc) HandlerFunc { middleware++; return next })
	p := principal.Principal{ID: "test", Scope: "project:test"}
	for _, method := range []string{"test.principal.unknown", "test.principal.max"} {
		r.Register(method, func(context.Context, json.RawMessage) (any, error) { calls++; return nil, nil })
		for _, ctx := range []context.Context{context.Background(), principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), principal.WithCaller(context.Background(), principal.Caller{Principal: &p})} {
			if err := checkAccess(ctx, declaredAccess(method)); err != errUnknownAccess || err.(*cascade.Error).Msg != "unknown access class" {
				t.Fatal(err)
			}
			_, err := r.Dispatch(ctx, &Request{Method: method})
			if err == nil || err.Code != cascade.RPCCodeInternal || err.Message != "unknown access class" || calls != 0 || middleware != 0 {
				t.Fatal(err, calls, middleware)
			}
		}
	}
}

func TestMalformedCallerHeadersRefused(t *testing.T) {
	m, clock, token := principalFixture(t)
	served := 0
	r := NewRegistry()
	r.SetAccessPolicy(func(tok string) (principal.Principal, error) { return m.Resolve(tok, clock.Now()) })
	r.Register("test.principal.scoped", func(context.Context, json.RawMessage) (any, error) { served++; return "ok", nil })
	cases := map[string]struct {
		headers map[string][]string
		code    int
	}{
		"duplicate token":  {map[string][]string{principal.HeaderToken: {token, token}}, 403},
		"empty token":      {map[string][]string{principal.HeaderToken: {""}}, 403},
		"duplicate marker": {map[string][]string{principal.HeaderOwnerCLI: {"owner-cli", "owner-cli"}}, 400},
		"other marker":     {map[string][]string{principal.HeaderOwnerCLI: {"owner"}}, 400},
		"control token":    {map[string][]string{principal.HeaderToken: {token}}, 200},
	}
	for name, tc := range cases {
		req := httptest.NewRequest("POST", "http://unix"+RPCPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test.principal.scoped"}`))
		req = req.WithContext(ctxWithPeerCred(ownerUID(), true))
		req.Header.Set("Content-Type", "application/json")
		for k, vs := range tc.headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		before := served
		w := httptest.NewRecorder()
		NewHandler(r).ServeHTTP(w, req)
		if w.Code != tc.code || (tc.code != 200) != (served == before) {
			t.Errorf("%s: code %d served %d, want %d", name, w.Code, served-before, tc.code)
		}
	}
}

func TestDeclaredAccessClassesInRange(t *testing.T) {
	n := 0
	for name, a := range methodAccess {
		if strings.HasPrefix(name, "test.") {
			continue
		}
		n++
		if a == AccessUnmarked || a > accessMax {
			t.Errorf("%s declared with class %d", name, a)
		}
	}
	if n < 40 {
		t.Fatalf("only %d production declarations seen", n)
	}
}
