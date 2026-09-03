package transport_test

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegisterSocketMCP_Dispatches drives registry.Dispatch directly
// (rather than a real listener) so this file stays in the no-network
// default test lane — no `net` import — while still proving mcp.dispatch
// is registered and bridges into the injected Dispatcher correctly. This
// test's outcome depends on GOOS: on windows, RegisterSocketMCP refuses
// (socket_windows.go) and mcp.dispatch is never registered; everywhere
// else it registers and dispatches for real.
func TestRegisterSocketMCP_Dispatches(t *testing.T) {
	reg := rpc.NewRegistry()
	err := transport.RegisterSocketMCP(reg, echoOK())

	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("want a refusal error on windows")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
			t.Fatalf("want KindUnsupported, got kind=%v ok=%v err=%v", kind, ok, err)
		}
		if reg.Registered(transport.MCPMethod) {
			t.Fatal("mcp.dispatch must not be registered on windows")
		}
		return
	}

	if err != nil {
		t.Fatalf("RegisterSocketMCP error = %v", err)
	}
	if !reg.Registered(transport.MCPMethod) {
		t.Fatal("mcp.dispatch was not registered")
	}

	params, marshalErr := json.Marshal(&mcp.Frame{
		JSONRPC: "2.0", Method: "tools/list",
		MCPMethod: "tools/list", MCPName: "c", ID: json.RawMessage("1"),
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	req := &rpc.Request{JSONRPC: "2.0", Method: transport.MCPMethod, Params: params, ID: json.RawMessage("1")}
	result, dispatchErr := reg.Dispatch(context.Background(), req)
	if dispatchErr != nil {
		t.Fatalf("registry.Dispatch error = %v", dispatchErr)
	}
	resp, ok := result.(*mcp.Response)
	if !ok {
		t.Fatalf("result type = %T, want *mcp.Response", result)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected mcp-level error: %v", resp.Error)
	}
}

// TestRegisterSocketMCP_MalformedParamsNeverReturnsHandlerError proves the
// bridged handler always succeeds at the JSON-RPC layer (per socket.go's
// doc comment): a malformed MCP frame surfaces as an mcp.Response.Error,
// never as a registry.Dispatch error, on every platform where registration
// itself succeeds.
func TestRegisterSocketMCP_MalformedParamsNeverReturnsHandlerError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mcp.dispatch is never registered on windows")
	}
	reg := rpc.NewRegistry()
	if err := transport.RegisterSocketMCP(reg, echoOK()); err != nil {
		t.Fatal(err)
	}
	req := &rpc.Request{JSONRPC: "2.0", Method: transport.MCPMethod, Params: json.RawMessage(`not json`), ID: json.RawMessage("1")}
	result, dispatchErr := reg.Dispatch(context.Background(), req)
	if dispatchErr != nil {
		t.Fatalf("registry.Dispatch error = %v, want nil (malformed MCP frame is an mcp.Response.Error)", dispatchErr)
	}
	resp := result.(*mcp.Response)
	if resp.Error == nil {
		t.Fatal("want a non-nil mcp.Response.Error for a malformed frame")
	}
}
