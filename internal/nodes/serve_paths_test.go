// Purpose: error-path tests for ServeRegistry.Call and Handler: params that
// cannot be encoded, a result that cannot be encoded or decoded, a call that
// ignores its result, and the HTTP handler the composition root mounts.
// SPORT: nodes tests (EXTEND) - P1-CI-13.
package nodes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"encoding/json"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestServeRegistryCallPaths(t *testing.T) {
	ctx := context.Background()
	sr := NewServeRegistry(ServeDeps{})
	sr.reg.Register("test.chan", func(context.Context, json.RawMessage) (any, error) { return make(chan int), nil })
	sr.reg.Register("test.text", func(context.Context, json.RawMessage) (any, error) { return "text", nil })
	t.Run("params that cannot be encoded", func(t *testing.T) {
		wantFailure(t, sr.Call(ctx, "test.text", make(chan int), nil), cascade.KindInvalidInput, "encode call params")
	})
	t.Run("result that cannot be encoded", func(t *testing.T) {
		var out any
		wantFailure(t, sr.Call(ctx, "test.chan", struct{}{}, &out), cascade.KindInternal, "encode call result")
	})
	t.Run("result that does not decode into out", func(t *testing.T) {
		var out int
		wantFailure(t, sr.Call(ctx, "test.text", struct{}{}, &out), cascade.KindIntegrity, "decode call result")
	})
	t.Run("a call that ignores its result", func(t *testing.T) {
		if err := sr.Call(ctx, "test.text", struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestServeRegistryHandlerRefusesUnknownRoute(t *testing.T) {
	h := NewServeRegistry(ServeDeps{}).Handler()
	if h == nil {
		t.Fatal("Handler returned nil")
	}
	req := httptest.NewRequest("POST", "/not-the-rpc-path", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status %d, want %d", rec.Code, 404)
	}
}
