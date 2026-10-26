package rpc

// Purpose: guardLocalRequest's owner-peer judgement over a handler with access
// policy: the refusal an agent sees, what the guard is never asked, the
// recycled or exited connector, and the pid that last used a connection.
// Constraints: see owner_peer_test.go (no "net"/"net/http" import; the pid and
// start time are injected through the context keys ConnContext fills).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ownerPeerFixture is a handler over a registry that serves three verbs, plus
// a counter of how often each reached its handler.
type ownerPeerFixture struct {
	h      *Handler
	served map[string]int
	token  string
}

func newOwnerPeerFixture(t *testing.T) *ownerPeerFixture {
	t.Helper()
	useStartTime(t, 7)
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	m := principal.NewMinter(clock)
	token, err := m.Mint(principal.Principal{ID: "peer-test", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &ownerPeerFixture{served: map[string]int{}, token: token}
	reg := NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) { return m.Resolve(tok, clock.Now()) })
	for _, name := range []string{"test.ownerpeer.owner", "test.ownerpeer.scoped"} {
		reg.Register(name, func(context.Context, json.RawMessage) (any, error) {
			f.served[name]++
			return "served", nil
		})
	}
	f.h = NewHandlerWithSSE(reg, nil)
	return f
}

// post sends one JSON-RPC call for method with the given extra headers, as a
// connection whose resolved peer is the owner uid at pid (pid 0 = unresolved).
func (f *ownerPeerFixture) post(method string, pid int, headers map[string]string) *httptest.ResponseRecorder {
	return f.postCtx(ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), pid, 7, pid > 0), method, headers)
}

// postCtx is post over a caller-built request context, on a connection armed
// as the daemon composition arms it (rpc.GuardedConnContext).
func (f *ownerPeerFixture) postCtx(ctx context.Context, method string, headers map[string]string) *httptest.ResponseRecorder {
	return f.postRaw(context.WithValue(ctx, peerArmedKey{}, true), method, headers)
}

// postRaw is postCtx without arming the connection.
func (f *ownerPeerFixture) postRaw(ctx context.Context, method string, headers map[string]string) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
	r := httptest.NewRequest("POST", RPCPath, strings.NewReader(body)).WithContext(ctx)
	r.Host = "unix"
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// wantRefusal asserts the exact refusal an agent process receives.
func wantRefusal(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 403 {
		t.Fatalf("%s: status %d body %q, want 403", name, w.Code, w.Body.String())
	}
	var env struct {
		Result any `json:"result"`
		Error  struct {
			Code    int               `json:"code"`
			Message string            `json:"message"`
			Data    map[string]string `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s: body %q is not an envelope: %v", name, w.Body.String(), err)
	}
	if env.Result != nil || env.Error.Code != cascade.RPCCodePermissionDenied ||
		env.Error.Message != "agent processes may not act as the owner" ||
		env.Error.Data["kind"] != cascade.KindPermissionDenied.String() {
		t.Errorf("%s: envelope %+v, want KindPermissionDenied %q", name, env, "agent processes may not act as the owner")
	}
}

func TestOwnerPeerGuardRefusesOwnerMarker(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t)
	owner := map[string]string{principal.HeaderOwnerCLI: "owner-cli"}

	rec := &peerRecorder{}
	SetOwnerPeerGuard(rec.guard)
	w := f.post("test.ownerpeer.owner", 4242, owner)
	if w.Code != 200 || f.served["test.ownerpeer.owner"] != 1 || len(rec.pids) != 1 || rec.pids[0] != 4242 {
		t.Fatalf("admitted peer: status %d served %v guard saw %v, want 200, one call, pid 4242", w.Code, f.served, rec.pids)
	}

	rec.err = errors.New("peer 4242 holds marker clm_secret")
	w = f.post("test.ownerpeer.owner", 4242, owner)
	wantRefusal(t, "guard error", w)
	if strings.Contains(w.Body.String(), "clm_secret") || strings.Contains(w.Body.String(), "4242") {
		t.Errorf("the refusal leaked the guard's reason: %q", w.Body.String())
	}
	if f.served["test.ownerpeer.owner"] != 1 {
		t.Errorf("a refused request reached the handler: %v", f.served)
	}

	rec.err = nil
	before := len(rec.pids)
	wantRefusal(t, "unknown pid", f.post("test.ownerpeer.owner", 0, owner))
	if len(rec.pids) != before || f.served["test.ownerpeer.owner"] != 1 {
		t.Errorf("an unknown pid reached the guard or the handler: guard %v served %v", rec.pids, f.served)
	}
}

func TestOwnerPeerGuardLeavesOtherRequestsAlone(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t)
	rec := &peerRecorder{err: errors.New("refuse everything judged")}
	SetOwnerPeerGuard(rec.guard)

	if w := f.post("test.ownerpeer.scoped", 0, map[string]string{principal.HeaderToken: f.token}); w.Code != 200 || f.served["test.ownerpeer.scoped"] != 1 {
		t.Errorf("principal request: status %d served %v, want 200 and served", w.Code, f.served)
	}
	both := map[string]string{principal.HeaderToken: f.token, principal.HeaderOwnerCLI: "owner-cli"}
	if w := f.post("test.ownerpeer.owner", 4242, both); w.Code != 400 || f.served["test.ownerpeer.owner"] != 0 {
		t.Errorf("both headers: status %d served %v, want admitCaller's 400", w.Code, f.served)
	}
	w := f.post("test.ownerpeer.owner", 4242, nil)
	var anon struct {
		Result any          `json:"result"`
		Error  *ErrorObject `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &anon); err != nil || w.Code != 200 || anon.Result != nil || anon.Error == nil ||
		anon.Error.Code != cascade.RPCCodePermissionDenied || f.served["test.ownerpeer.owner"] != 0 {
		t.Errorf("anonymous request: status %d body %q served %v, want the access policy's permission-denied and no dispatch", w.Code, w.Body.String(), f.served)
	}
	if len(rec.pids) != 0 {
		t.Errorf("the guard judged requests it does not own: pids %v", rec.pids)
	}
}

func TestOwnerPeerGuardAbsentChangesNothing(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t)
	owner := map[string]string{principal.HeaderOwnerCLI: "owner-cli"}
	unarmed := ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), 4242, 7, true)
	w := f.postRaw(unarmed, "test.ownerpeer.owner", owner)
	if w.Code != 200 || f.served["test.ownerpeer.owner"] != 1 {
		t.Errorf("unarmed, no guard: status %d served %v, want the owner view as before", w.Code, f.served)
	}
	rec := &peerRecorder{err: errors.New("refuse")}
	SetOwnerPeerGuard(rec.guard)
	w = f.postRaw(unarmed, "test.ownerpeer.owner", owner)
	if w.Code != 200 || f.served["test.ownerpeer.owner"] != 2 || len(rec.pids) != 0 {
		t.Errorf("unarmed, guard installed: status %d served %v judged %v, want served and not judged", w.Code, f.served, rec.pids)
	}
}

func TestOwnerPeerGuardRefusesOnEventsRoute(t *testing.T) {
	resetOwnerPeerGuard(t)
	useStartTime(t, 7)
	h := newTestEventsHandler(t)
	rec := &peerRecorder{err: errors.New("agent")}
	SetOwnerPeerGuard(rec.guard)
	owner := map[string]string{"Host": "unix", principal.HeaderOwnerCLI: "owner-cli"}

	// A broken guard would admit the request and stream until the context ends:
	// a short deadline turns that into a failed assertion instead of a hang.
	base, cancel := context.WithTimeout(context.WithValue(ctxWithPeerCred(ownerUID(), true), peerArmedKey{}, true), 2*time.Second)
	defer cancel()
	w := eventsReq(ctxWithPeerPID(base, 4242, 7, true), h, "unix", owner)
	wantRefusal(t, "events, guard error", w)
	if len(rec.pids) != 1 || rec.pids[0] != 4242 {
		t.Errorf("the guard saw %v, want [4242]", rec.pids)
	}
	wantRefusal(t, "events, unknown pid", eventsReq(base, h, "unix", owner))
	if len(rec.pids) != 1 {
		t.Errorf("an unknown pid reached the guard: %v", rec.pids)
	}
}

func TestOwnerPeerGuardRefusesARecycledOrExitedConnector(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t) // every pid started at 7 when the connection was accepted
	rec := &peerRecorder{}
	SetOwnerPeerGuard(rec.guard)
	owner := map[string]string{principal.HeaderOwnerCLI: "owner-cli"}

	if w := f.post("test.ownerpeer.owner", 4242, owner); w.Code != 200 || len(rec.pids) != 1 {
		t.Fatalf("same connector: status %d guard calls %v, want admitted by the guard", w.Code, rec.pids)
	}
	peerStartTime = func(int) (uint64, bool) { return 8, true } // the pid now names another process
	wantRefusal(t, "recycled pid", f.post("test.ownerpeer.owner", 4242, owner))
	peerStartTime = func(int) (uint64, bool) { return 0, false } // the connector is gone
	wantRefusal(t, "exited connector", f.post("test.ownerpeer.owner", 4242, owner))
	if len(rec.pids) != 1 || f.served["test.ownerpeer.owner"] != 1 {
		t.Errorf("a recycled or exited connector reached the guard or the handler: guard %v served %v", rec.pids, f.served)
	}

	// A start time that could not be read when the connection was accepted is never trusted.
	peerStartTime = func(int) (uint64, bool) { return 7, true }
	unread := ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), 4242, 0, true)
	wantRefusal(t, "start time unreadable at accept", f.postCtx(unread, "test.ownerpeer.owner", owner))
}

func TestOwnerPeerGuardRefusesAConnectorRecycledDuringTheGuard(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t) // every pid started at 7 when the connection was accepted
	calls := 0
	SetOwnerPeerGuard(func(context.Context, int) error {
		calls++
		useStartTime(t, 9) // the pid is recycled while the guard reads the peer
		return nil
	})
	owner := map[string]string{principal.HeaderOwnerCLI: "owner-cli"}
	wantRefusal(t, "recycled during the guard", f.post("test.ownerpeer.owner", 4242, owner))
	if calls != 1 || f.served["test.ownerpeer.owner"] != 0 {
		t.Errorf("guard calls %d served %v, want one call and no dispatch", calls, f.served)
	}
}

func TestOwnerPeerGuardJudgesThePidThatLastUsedTheConnection(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t)
	owner := map[string]string{principal.HeaderOwnerCLI: "owner-cli"}
	now := func(pid int, ok bool) context.Context {
		base := ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), 4242, 7, true)
		return context.WithValue(base, peerNowKey{}, func() (int, bool) { return pid, ok })
	}
	var judged []int
	SetOwnerPeerGuard(func(_ context.Context, pid int) error {
		judged = append(judged, pid)
		if pid == 5151 {
			return errors.New("agent")
		}
		if pid == 7373 {
			useStartTime(t, 9) // this pid is recycled while the guard reads it
		}
		return nil
	})

	// Same pid at accept and now: judged once, admitted.
	if w := f.postCtx(now(4242, true), "test.ownerpeer.owner", owner); w.Code != 200 || len(judged) != 1 {
		t.Fatalf("same pid: status %d judged %v, want admitted after one judgement", w.Code, judged)
	}
	// A different pid now, clean: both are judged, admitted.
	judged = nil
	if w := f.postCtx(now(6262, true), "test.ownerpeer.owner", owner); w.Code != 200 || len(judged) != 2 || judged[0] != 4242 || judged[1] != 6262 {
		t.Fatalf("clean second pid: status %d judged %v, want [4242 6262] and admitted", w.Code, judged)
	}
	// A different pid now that the guard refuses: refused, never served.
	served := f.served["test.ownerpeer.owner"]
	wantRefusal(t, "agent wrote on the connection", f.postCtx(now(5151, true), "test.ownerpeer.owner", owner))
	// The kernel cannot answer the second time: refused.
	wantRefusal(t, "second lookup failed", f.postCtx(now(0, false), "test.ownerpeer.owner", owner))
	// The second pid is recycled while the guard reads it: refused (kept last, it changes every start time).
	wantRefusal(t, "second pid recycled during the guard", f.postCtx(now(7373, true), "test.ownerpeer.owner", owner))
	if f.served["test.ownerpeer.owner"] != served {
		t.Errorf("a refused request reached the handler: %v", f.served)
	}
}

func TestOwnerPeerGuardAbsentOnArmedConnectionRefuses(t *testing.T) {
	resetOwnerPeerGuard(t)
	f := newOwnerPeerFixture(t)
	armed := context.WithValue(ctxWithPeerPID(ctxWithPeerCred(ownerUID(), true), 4242, 7, true), peerArmedKey{}, true)
	wantRefusal(t, "armed connection, no guard", f.postCtx(armed, "test.ownerpeer.owner", map[string]string{principal.HeaderOwnerCLI: "owner-cli"}))
	if f.served["test.ownerpeer.owner"] != 0 {
		t.Errorf("an armed connection with no guard served the owner view: %v", f.served)
	}
}
