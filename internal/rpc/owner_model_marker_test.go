package rpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
)

var _ = DeclareAccess("test.model.view", AccessOwnerView)

func TestOwnerModelMarkerResolvesOwnerViewWithoutLocalOnly(t *testing.T) {
	reg := NewRegistry()
	reg.SetAccessPolicy(nil)
	calls := 0
	var caller principal.Caller
	reg.Register("test.model.view", func(ctx context.Context, _ json.RawMessage) (any, error) {
		var ok bool
		caller, ok = principal.CallerFrom(ctx)
		if !ok {
			t.Error("missing caller")
		}
		calls++
		return true, nil
	})
	for _, marker := range []string{"owner-model", "owner-cli", "OWNER-CLI", "Owner-Model", " owner-model", "owner-model ", "owner-model, owner-cli", "owner-cli2", "", "repeated", "both"} {
		t.Run(marker, func(t *testing.T) {
			calls = 0
			r := httptest.NewRequest("POST", "http://unix/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test.model.view"}`))
			r = r.WithContext(ctxWithPeerCred(ownerUID(), true))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(principal.HeaderOwnerCLI, marker)
			if marker == "repeated" {
				r.Header[principal.HeaderOwnerCLI] = []string{"owner-model", "owner-cli"}
			}
			if marker == "both" {
				r.Header.Set(principal.HeaderOwnerCLI, "owner-model")
				r.Header.Set(principal.HeaderToken, "test-principal")
			}
			w := httptest.NewRecorder()
			NewHandler(reg).ServeHTTP(w, r)
			if marker == "owner-model" || marker == "owner-cli" {
				if w.Code != 200 || calls != 1 || caller.Principal != nil || caller.OwnerCLI != (marker == "owner-cli") {
					t.Fatal(w.Code, calls, caller)
				}
				return
			}
			message := "invalid client marker"
			if marker == "both" {
				message = "conflicting caller headers"
			}
			if w.Code != 400 || calls != 0 || !strings.Contains(w.Body.String(), `"message":"`+message+`"`) || !strings.Contains(w.Body.String(), `"kind":"invalid-input"`) {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}
