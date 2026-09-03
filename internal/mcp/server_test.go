package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/plugin"

	_ "github.com/acamarata/cascade/plugins/examples/example-builtin"
)

func emptyRegistry() *mcp.ToolRegistry {
	return mcp.NewToolRegistry(func() []plugin.BuiltinRegistration { return nil })
}

func mustID(t *testing.T, n int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDispatch_MissingMcpHeaders(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{JSONRPC: "2.0", Method: mcp.MethodToolsList, ID: mustID(t, 1)}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error == nil {
		t.Fatal("want an error response for missing mcp_method/mcp_name")
	}
}

func TestDispatch_MismatchedMcpMethod(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsList,
		MCPMethod: "tools/call", MCPName: "test-client", ID: mustID(t, 1),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error == nil {
		t.Fatal("want an error response for mismatched mcp_method")
	}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: "bogus/method",
		MCPMethod: "bogus/method", MCPName: "test-client", ID: mustID(t, 1),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error == nil {
		t.Fatal("want an error response for an unknown method")
	}
}

func TestDispatch_ToolsList_Empty(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsList,
		MCPMethod: mcp.MethodToolsList, MCPName: "test-client", ID: mustID(t, 1),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestDispatch_ToolsCall_MissingName(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall,
		MCPMethod: mcp.MethodToolsCall, MCPName: "test-client", ID: mustID(t, 1),
		Params: json.RawMessage(`{}`),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error == nil {
		t.Fatal("want an error response for tools/call with no name")
	}
}

func TestDispatch_ToolsCall_UnknownTool(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall,
		MCPMethod: mcp.MethodToolsCall, MCPName: "test-client", ID: mustID(t, 1),
		Params: json.RawMessage(`{"name":"nope"}`),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error == nil {
		t.Fatal("want an error response for an unknown tool")
	}
}

// TestMCPToolCallable is the contract's required end-to-end test: a real
// registered builtin plugin (example-builtin, C-S05.T6) is called through
// the full registry -> Server.Dispatch path, tools/list -> tools/call ->
// result, exactly as a client would drive it.
func TestMCPToolCallable(t *testing.T) {
	s := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins))

	list := s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsList,
		MCPMethod: mcp.MethodToolsList, MCPName: "test-client", ID: mustID(t, 1),
	})
	if list.Error != nil {
		t.Fatalf("tools/list error: %v", list.Error)
	}
	body, err := json.Marshal(list.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolName(t, body, "greet-tool") {
		t.Fatalf("tools/list result %s does not contain greet-tool", body)
	}

	call := s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall,
		MCPMethod: mcp.MethodToolsCall, MCPName: "test-client", ID: mustID(t, 2),
		Params: json.RawMessage(`{"name":"greet-tool","arguments":"Cascade"}`),
	})
	if call.Error != nil {
		t.Fatalf("tools/call error: %v", call.Error)
	}
}

func containsToolName(t *testing.T, body []byte, name string) bool {
	t.Helper()
	var v struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	for _, tool := range v.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// TestDispatch_MRTR_NotificationsAck exercises the MRTR exchange the
// contract requires: a stateless follow-up call to notifications/ack,
// correlated only by data the client already holds (never a server-
// initiated push — see server.go's triggeredRequest doc comment for why
// MRTR is modeled this way).
func TestDispatch_MRTR_NotificationsAck(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	f := &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodNotificationsAck,
		MCPMethod: mcp.MethodNotificationsAck, MCPName: "test-client", ID: mustID(t, 1),
		Params: json.RawMessage(`{"trigger_id":"t-1"}`),
	}
	resp := s.Dispatch(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestParseFrame_MalformedJSON(t *testing.T) {
	if _, errObj := mcp.ParseFrame([]byte(`{not json`)); errObj == nil {
		t.Fatal("want an error for malformed JSON")
	}
}

func TestParseFrame_TruncatedInput(t *testing.T) {
	if _, errObj := mcp.ParseFrame([]byte(`{"jsonrpc":"2.0","method":`)); errObj == nil {
		t.Fatal("want an error for truncated input")
	}
}

func TestParseFrame_MissingMethod(t *testing.T) {
	if _, errObj := mcp.ParseFrame([]byte(`{"jsonrpc":"2.0"}`)); errObj == nil {
		t.Fatal("want an error for missing method")
	}
}

func TestParseFrame_WrongJSONRPCVersion(t *testing.T) {
	if _, errObj := mcp.ParseFrame([]byte(`{"jsonrpc":"1.0","method":"tools/list"}`)); errObj == nil {
		t.Fatal("want an error for jsonrpc != 2.0")
	}
}

// TestMCPGoldens asserts Dispatch's tools/list response against
// testdata/tools_list.golden.json. IMPORTANT: per testdata/README.md's
// documented gap, this fixture is self-authored from the ticket contract's
// own text, NOT captured from a real rmcp 3.0.1 client — this build
// environment had no network access and no rmcp toolchain available. This
// test still proves the pinned revision's wire SHAPE is stable and
// regression-tested; it does not satisfy Art.2's real-counterpart
// requirement for the literal bytes. See testdata/README.md for the
// follow-up capture this gap requires.
func TestMCPGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/tools_list.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Request  mcp.Frame       `json:"request"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	s := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins))
	got := s.Dispatch(context.Background(), &golden.Request)
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var gotNorm, wantNorm any
	if err := json.Unmarshal(gotBytes, &gotNorm); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(golden.Response, &wantNorm); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(gotNorm)
	wantJSON, _ := json.Marshal(wantNorm)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("tools/list golden mismatch:\n got:  %s\n want: %s", gotJSON, wantJSON)
	}
}
