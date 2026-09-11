package daemon

// Purpose: unit coverage for RegisterPolicyHandlers and adaptPolicyHandler:
//   the two refusal branches (nil registry, empty handler set), and the
//   real registration-and-dispatch path, driven through a real
//   *rpc.Registry.Dispatch (the fleet.rpc_test.go precedent).
// SPORT: internal/daemon (ADD, coverage-floor fix).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegisterPolicyHandlers_NilRegistryRefuses proves the nil-registry
// guard returns a real KindInvalidInput error rather than panicking on a
// nil dereference.
func TestRegisterPolicyHandlers_NilRegistryRefuses(t *testing.T) {
	err := RegisterPolicyHandlers(nil, map[string]policy.MethodFunc{
		"policy.check": func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("RegisterPolicyHandlers(nil registry, ...) = nil, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("error kind: got %v, want KindInvalidInput", err)
	}
}

// TestRegisterPolicyHandlers_EmptyHandlerSetRefuses proves an empty
// handler map is refused rather than silently registering an empty,
// indistinguishable-from-never-wired namespace.
func TestRegisterPolicyHandlers_EmptyHandlerSetRefuses(t *testing.T) {
	registry := rpc.NewRegistry()
	err := RegisterPolicyHandlers(registry, map[string]policy.MethodFunc{})
	if err == nil {
		t.Fatal("RegisterPolicyHandlers(registry, empty map) = nil, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("error kind: got %v, want KindInvalidInput", err)
	}
}

// TestRegisterPolicyHandlers_RealDispatch proves every handler in the map
// reaches the real registry and is called through Dispatch, unmodified:
// adaptPolicyHandler's own result and error are passed through as-is.
func TestRegisterPolicyHandlers_RealDispatch(t *testing.T) {
	registry := rpc.NewRegistry()
	wantResult := map[string]string{"status": "allowed"}
	var gotParams json.RawMessage
	handlers := map[string]policy.MethodFunc{
		"policy.check": func(_ context.Context, params json.RawMessage) (any, error) {
			gotParams = params
			return wantResult, nil
		},
		"policy.fails": func(context.Context, json.RawMessage) (any, error) {
			return nil, cascade.New(cascade.KindPolicyDenied, "policy: denied by rule 7")
		},
	}
	if err := RegisterPolicyHandlers(registry, handlers); err != nil {
		t.Fatalf("RegisterPolicyHandlers: unexpected error %v", err)
	}

	sentParams := json.RawMessage(`{"actor":"t1"}`)
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "policy.check", Params: sentParams})
	if errObj != nil {
		t.Fatalf("Dispatch(policy.check) error = %+v, want nil", errObj)
	}
	res, ok := result.(map[string]string)
	if !ok || res["status"] != "allowed" {
		t.Errorf("Dispatch(policy.check) result = %#v, want %#v", result, wantResult)
	}
	if string(gotParams) != string(sentParams) {
		t.Errorf("adaptPolicyHandler passed params = %s, want %s", gotParams, sentParams)
	}

	_, errObj = registry.Dispatch(context.Background(), &rpc.Request{Method: "policy.fails", Params: nil})
	if errObj == nil {
		t.Fatal("Dispatch(policy.fails) error = nil, want the real denial")
	}
	kind, ok := cascade.KindFromJSONRPCCode(errObj.Code)
	if !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("Dispatch(policy.fails) kind = %v (ok=%v), want KindPolicyDenied", kind, ok)
	}
}
