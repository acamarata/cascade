// Package conformance (fixture_dispatch_test.go): Purpose: the seven
// per-host-fn callRefSink* functions callRefSinkMethod
// (fixture_runner_test.go) routes to. Split out to keep
// fixture_runner_test.go under the repo's 300-line file cap, mirroring
// wasm/host_abi_core.go's own groupA/groupB split for the same reason.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/acamarata/cascade/internal/plugins/wasm"
	"github.com/acamarata/cascade/pkg/cascade"
)

func errUnknownMethod(method string) error {
	return cascade.Newf(cascade.KindInvalidInput, "conformance: unknown ABI method %q", method)
}

// checkConformanceNetScope mirrors wasm/host_abi_net.go's real
// checkNetScope algorithm against wasmHarnessNetScopes (harness_wazero_test.go).
// A builtin plugin crosses no wasm boundary, but host_http_request's
// scope check is a general host-side boundary every runtime applies
// before invoking its network delegate, not a wasm-specific concern --
// builtinHarness enforcing it here is what makes the host_http/error
// fixture (and TestConformance_AllRuntimesAgree's comparison of it) a
// real, symmetric check rather than a builtin-only pass-through. Found
// by this ticket's own first suite run: without this check,
// TestConformance_BuiltinRuntime/host_http/error failed with "error-path
// call returned no error" -- refSink.Do always succeeds, so the scope
// boundary has to be enforced upstream of it, exactly as wasm's real
// Dispatch does upstream of Deps.Net.
func checkConformanceNetScope(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "conformance: host_http: %q is not a well-formed URL", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	for _, scope := range wasmHarnessNetScopes {
		if conformanceScopeMatches(strings.ToLower(scope), host) {
			return nil
		}
	}
	return cascade.New(cascade.KindPermissionDenied, "conformance: host_http: target URL is outside the plugin's declared net scope")
}

// conformanceScopeMatches: a bare hostname matches exactly; "*.example.com"
// matches any subdomain of example.com but not example.com itself.
func conformanceScopeMatches(scope, host string) bool {
	if scope == "" {
		return false
	}
	if strings.HasPrefix(scope, "*.") {
		suffix := scope[1:]
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return scope == host
}

func callRefSinkLog(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.LogRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	if err := sink.Log(ctx, req.Level, req.Message); err != nil {
		return nil, err
	}
	return wasm.LogResponse{Accepted: true}, nil
}

func callRefSinkHTTP(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.HTTPRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	if err := checkConformanceNetScope(req.URL); err != nil {
		return nil, err
	}
	resp, err := sink.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func callRefSinkStream(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.StreamRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	n, err := sink.Write(ctx, req.ChannelID, req.Data)
	if err != nil {
		return nil, err
	}
	return wasm.StreamResponse{BytesWritten: n}, nil
}

func callRefSinkSecretRef(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.SecretRefRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "conformance: host_secretref: name is empty")
	}
	refID, err := sink.CreateRef(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return wasm.SecretRefResponse{RefID: refID}, nil
}

func callRefSinkEventEmit(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.EventEmitRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	id, err := sink.Emit(ctx, req.Topic, req.Payload)
	if err != nil {
		return nil, err
	}
	return wasm.EventEmitResponse{EventID: id}, nil
}

func callRefSinkToolRegister(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.ToolRegisterRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	if err := sink.Register(ctx, req.ToolName, req.Schema); err != nil {
		return nil, err
	}
	return wasm.ToolRegisterResponse{Registered: true}, nil
}

// callRefSinkStorage multiplexes StorageRequest.Op across get/put/
// delete/list, mirroring wasm/host_abi_storage.go's dispatchStorage
// exactly (same op names, same response shape) so the two are directly
// comparable.
func callRefSinkStorage(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	var req wasm.StorageRequest
	if err := json.Unmarshal(f.WasmRequest, &req); err != nil {
		return nil, err
	}
	switch req.Op {
	case "get":
		v, err := sink.Get(ctx, req.Key)
		if err != nil {
			return nil, err
		}
		return wasm.StorageResponse{Value: v}, nil
	case "put":
		if err := sink.Set(ctx, req.Key, req.Value); err != nil {
			return nil, err
		}
		return wasm.StorageResponse{}, nil
	case "delete":
		if err := sink.Delete(ctx, req.Key); err != nil {
			return nil, err
		}
		return wasm.StorageResponse{}, nil
	case "list":
		keys, err := sink.List(ctx, req.Key)
		if err != nil {
			return nil, err
		}
		return wasm.StorageResponse{Keys: keys}, nil
	default:
		return nil, cascade.Newf(cascade.KindInvalidInput, "conformance: host_storage: unrecognized op %q", req.Op)
	}
}
