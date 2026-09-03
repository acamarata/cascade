package rpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// withOwnerUID overrides ownerUID for the duration of one test, restoring
// it on cleanup — a spoofable seam so tests never depend on the real
// process UID (Art.7.1: no real network listener / privileged operation
// outside a controlled seam).
func withOwnerUID(t *testing.T, uid int) {
	t.Helper()
	prev := ownerUID
	ownerUID = func() int { return uid }
	t.Cleanup(func() { ownerUID = prev })
}

// ctxWithPeerCred builds a request context carrying a spoofed peerCred
// directly (bypassing ConnContext's real syscall path entirely), which is
// exactly what this ticket's AC calls for: "tested with spoofed
// ConnContext in unit test."
func ctxWithPeerCred(uid int, ok bool) context.Context {
	return context.WithValue(context.Background(), peerCredKey{}, peerCred{uid: uid, ok: ok})
}

func newTestHandler() (*Handler, *Registry) {
	reg := NewRegistry()
	return NewHandler(reg), reg
}

func doRPC(ctx context.Context, h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", RPCPath, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler_UIDRejected(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := doRPC(ctxWithPeerCred(999, true), h, `{"jsonrpc":"2.0","method":"m","id":1}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403 for non-owner UID", rec.Code)
	}
}

func TestHandler_UIDResolutionFailedRejected(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	// ok=false (peer credential could not be resolved at all) must ALSO be
	// rejected — fail-closed, never fail-open.
	rec := doRPC(ctxWithPeerCred(501, false), h, `{"jsonrpc":"2.0","method":"m","id":1}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403 when peer credential resolution failed", rec.Code)
	}
}

func TestHandler_RoundTrip(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("ping", func(_ context.Context, _ json.RawMessage) (any, error) {
		return "pong", nil
	})

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"ping","id":1}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var env ResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if env.ProtocolVersion == "" || env.ServerVersion == "" {
		t.Error("envelope must carry protocol_version and server_version")
	}
	if env.Result != "pong" {
		t.Errorf("Result = %v, want pong", env.Result)
	}
}

func TestHandler_UnknownMethod(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"nope","id":1}`)
	var env ResponseEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != codeMethodNotFound {
		t.Fatalf("Error = %+v, want code %d", env.Error, codeMethodNotFound)
	}
}

func TestHandler_VersionSkew(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("m", func(_ context.Context, _ json.RawMessage) (any, error) { return "ok", nil })

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"m","id":1,"client_version":"999.0.0"}`)
	var env ResponseEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != codeInvalidRequest {
		t.Fatalf("Error = %+v, want code %d (skew)", env.Error, codeInvalidRequest)
	}
}

func TestHandler_ElevationDeniedThenReplayRejected(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("vault.get", func(_ context.Context, _ json.RawMessage) (any, error) {
		return "secret", nil
	})
	clock := runtime.NewFixedClock(time.Unix(3000, 0))
	ledger := NewNonceLedger(clock)
	trust := MapTrustStore{}
	reg.Use(ElevationMiddleware(ledger, trust, clock))

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"vault.get","id":1}`)
	var env ResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || env.Error.Code != codeElevationRequired {
		t.Fatalf("Error = %+v, want ELEVATION_REQUIRED (%d)", env.Error, codeElevationRequired)
	}
	data, ok := env.Error.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %#v, want a map carrying nonce", env.Error.Data)
	}
	nonce, _ := data["nonce"].(string)
	if nonce == "" {
		t.Fatal("ELEVATION_REQUIRED response must carry a non-empty nonce")
	}

	// A second, direct Consume of the SAME nonce must fail — the
	// single-use ledger proof this ticket's AC requires, exercised
	// through the same object the handler used.
	if err := ledger.Consume(nonce, "vault.get", hashParams(nil), clock.Now()); err != nil {
		t.Fatalf("first direct Consume should still succeed: %v", err)
	}
	if err := ledger.Consume(nonce, "vault.get", hashParams(nil), clock.Now()); err == nil {
		t.Fatal("replaying the same nonce must fail")
	}
}

func TestHandler_NonElevatedMethodPassesThroughElevationMiddleware(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("status.get", func(_ context.Context, _ json.RawMessage) (any, error) {
		return "ok", nil
	})
	clock := runtime.NewFixedClock(time.Unix(3000, 0))
	reg.Use(ElevationMiddleware(NewNonceLedger(clock), MapTrustStore{}, clock))

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"status.get","id":1}`)
	var env ResponseEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error != nil {
		t.Fatalf("non-elevated method must pass through: %+v", env.Error)
	}
	if env.Result != "ok" {
		t.Errorf("Result = %v, want ok", env.Result)
	}
}

func TestHandler_MalformedBody(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()
	rec := doRPC(ctxWithPeerCred(501, true), h, `{not json`)
	var env ResponseEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == nil || env.Error.Code != codeParseError {
		t.Fatalf("Error = %+v, want codeParseError", env.Error)
	}
}

func TestHandler_WrongPathIs404(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()
	req := httptest.NewRequest("POST", "/not-rpc", strings.NewReader("")).WithContext(ctxWithPeerCred(501, true))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_NotificationGetsNilID(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("m", func(_ context.Context, _ json.RawMessage) (any, error) { return "ok", nil })

	// No "id" key at all: a notification.
	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"m"}`)
	var env ResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.ID != nil {
		t.Errorf("ID = %v, want nil for a notification", env.ID)
	}
}

func TestOSGetuid_Production(_ *testing.T) {
	// osGetuid is the production ownerUID implementation, overridden by
	// withOwnerUID in every other test; this exercises the real one
	// directly (it must simply return SOME int without panicking).
	_ = osGetuid()
}
