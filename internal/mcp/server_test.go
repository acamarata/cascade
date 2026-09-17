package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/plugin"

	_ "github.com/acamarata/cascade/plugins/examples/example-builtin"
)

// Purpose (this file): the MCP message core, asserted against the protocol
//   a REAL client speaks — not against a description of one.
//
// Every test that used to live here asserted the invented
//   mcp_method/mcp_name dialect. That dialect was rejected by the only
//   client that exists (R-14.246), so those assertions were proving the
//   wrong thing correctly. They are replaced, not adjusted.
// SPORT: internal/mcp tests [CHANGE] — P1-E04-W4-S86-T1.

func emptyRegistry() *mcp.ToolRegistry {
	return mcp.NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, mcp.AllowAllFilter{})
}

func mustID(t *testing.T, n int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// resultOf re-marshals a response's result so a test can assert on the
// exact wire shape rather than on Go types the client never sees.
func resultOf(t *testing.T, resp *mcp.Response) map[string]any {
	t.Helper()
	if resp == nil {
		t.Fatal("no response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected protocol error: %v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestInitializeAnswersTheRealClientsHandshake replays the EXACT first
// frame the real client sent, read from the captured baseline rather than
// retyped here: a second copy of the evidence is a second thing to drift.
// Before R-14.246 this frame was answered with -32600, which is why no
// client could ever connect.
func TestInitializeAnswersTheRealClientsHandshake(t *testing.T) {
	line := readFrames(t, "baseline-in.jsonl")[0]

	frame, perr := mcp.ParseFrame(line)
	if perr != nil {
		t.Fatalf("the real client's own frame did not parse: %v", perr)
	}
	got := resultOf(t, mcp.NewServer(emptyRegistry()).Dispatch(context.Background(), frame))

	if got["protocolVersion"] != mcp.MCPProtocolVersion {
		t.Errorf("protocolVersion = %v, want %q", got["protocolVersion"], mcp.MCPProtocolVersion)
	}
	info, _ := got["serverInfo"].(map[string]any)
	if info == nil || info["name"] != "cascade" {
		t.Errorf("serverInfo = %v, want it to name cascade", got["serverInfo"])
	}
	caps, _ := got["capabilities"].(map[string]any)
	if caps == nil {
		t.Fatalf("capabilities = %v, want a tools capability", got["capabilities"])
	}
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities = %v, want tools declared", caps)
	}
	// Nothing else may be advertised: a declared capability whose calls
	// would all be method-not-found is worse than an undeclared one.
	if len(caps) != 1 {
		t.Errorf("capabilities = %v, want tools and nothing else", caps)
	}
}

// TestAnUnfamiliarVersionStillGetsAnAnswer holds the negotiation rule. A
// server that hung up on an unknown revision would break on whatever
// version every client ships before this one does — and refusing the
// handshake is exactly what made the previous wire unusable.
func TestAnUnfamiliarVersionStillGetsAnAnswer(t *testing.T) {
	for _, offered := range []string{"2026-07-28", "1999-01-01", ""} {
		frame := &mcp.Frame{
			JSONRPC: "2.0", Method: mcp.MethodInitialize, ID: mustID(t, 1),
			Params: json.RawMessage(`{"protocolVersion":"` + offered + `"}`),
		}
		got := resultOf(t, mcp.NewServer(emptyRegistry()).Dispatch(context.Background(), frame))
		if got["protocolVersion"] != mcp.MCPProtocolVersion {
			t.Errorf("offered %q: protocolVersion = %v, want cascade's own %q so the client can decide",
				offered, got["protocolVersion"], mcp.MCPProtocolVersion)
		}
	}
}

// TestANotificationIsAnsweredWithSilence is JSON-RPC 2.0's rule, and it is
// load-bearing: a real client treats an unsolicited message as a protocol
// violation. `notifications/initialized` is the one every client sends
// immediately after the handshake.
func TestANotificationIsAnsweredWithSilence(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	for _, method := range []string{"notifications/initialized", "notifications/cancelled", "notifications/anything"} {
		if resp := s.Dispatch(context.Background(), &mcp.Frame{JSONRPC: "2.0", Method: method}); resp != nil {
			t.Errorf("%s produced a response %+v; a notification must be answered with silence", method, resp)
		}
	}
	// An explicit null id is a notification too.
	if resp := s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: "notifications/initialized", ID: json.RawMessage("null"),
	}); resp != nil {
		t.Errorf("a null-id frame produced %+v, want silence", resp)
	}
}

// TestToolsListCarriesAnInputSchema holds the field a client needs to call
// a tool at all. An entry without it is one the model cannot invoke.
func TestToolsListCarriesAnInputSchema(t *testing.T) {
	s := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{}))
	got := resultOf(t, s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsList, ID: mustID(t, 1),
	}))
	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("tools/list returned %v, want the registered builtins", got)
	}
	found := false
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if tool == nil {
			t.Fatalf("tool entry %v is not an object", entry)
		}
		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %v has no inputSchema; a client cannot call it", tool["name"])
		}
		if tool["name"] == "greet-tool" {
			found = true
		}
	}
	if !found {
		t.Errorf("tools/list does not contain greet-tool: %v", tools)
	}
}

// TestToolsListIsDeterministic keeps a cached listing stable.
func TestToolsListIsDeterministic(t *testing.T) {
	s := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{}))
	first, _ := json.Marshal(s.Dispatch(context.Background(),
		&mcp.Frame{JSONRPC: "2.0", Method: mcp.MethodToolsList, ID: mustID(t, 1)}).Result)
	second, _ := json.Marshal(s.Dispatch(context.Background(),
		&mcp.Frame{JSONRPC: "2.0", Method: mcp.MethodToolsList, ID: mustID(t, 2)}).Result)
	if string(first) != string(second) {
		t.Errorf("tools/list is not deterministic:\n %s\n %s", first, second)
	}
}

// TestToolsCallReturnsContentBlocks holds the result shape. MCP has no
// "raw JSON result": a tool's output reaches the model as content blocks.
func TestToolsCallReturnsContentBlocks(t *testing.T) {
	s := mcp.NewServer(mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{}))
	got := resultOf(t, s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall, ID: mustID(t, 2),
		Params: json.RawMessage(`{"name":"greet-tool","arguments":"Cascade"}`),
	}))
	content, _ := got["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call result = %v, want at least one content block", got)
	}
	block, _ := content[0].(map[string]any)
	if block == nil || block["type"] != "text" {
		t.Errorf("content[0] = %v, want a text block", content[0])
	}
	if got["isError"] != false {
		t.Errorf("isError = %v, want false for a tool that succeeded", got["isError"])
	}
}

// TestAnUnknownToolIsAProtocolError keeps the two failure kinds apart: a
// missing tool is a protocol error, not a tool result the model should try
// to reason about.
func TestAnUnknownToolIsAProtocolError(t *testing.T) {
	s := mcp.NewServer(emptyRegistry())
	resp := s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall, ID: mustID(t, 1),
		Params: json.RawMessage(`{"name":"nope"}`),
	})
	if resp == nil || resp.Error == nil {
		t.Fatalf("resp = %+v, want a protocol error for an unknown tool", resp)
	}
	// And a call with no name at all is invalid params, not a tool lookup.
	missing := s.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: mcp.MethodToolsCall, ID: mustID(t, 2),
	})
	if missing == nil || missing.Error == nil {
		t.Fatalf("resp = %+v, want an error for a nameless tools/call", missing)
	}
}

// TestAnUnknownMethodIsMethodNotFound covers the routing default.
func TestAnUnknownMethodIsMethodNotFound(t *testing.T) {
	resp := mcp.NewServer(emptyRegistry()).Dispatch(context.Background(),
		&mcp.Frame{JSONRPC: "2.0", Method: "resources/list", ID: mustID(t, 1)})
	if resp == nil || resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("resp = %+v, want -32601 method not found", resp)
	}
}

// TestParseFrameRejectsMalformedInput covers the decoder's own refusals.
func TestParseFrameRejectsMalformedInput(t *testing.T) {
	for name, line := range map[string]string{
		"malformed JSON": `{"jsonrpc":`,
		"truncated":      `{"jsonrpc":"2.0","method":`,
		"no method":      `{"jsonrpc":"2.0","id":1}`,
		"wrong jsonrpc":  `{"jsonrpc":"1.0","method":"tools/list","id":1}`,
		"not an object":  `["tools/list"]`,
		"empty":          ``,
	} {
		if _, err := mcp.ParseFrame([]byte(line)); err == nil {
			t.Errorf("%s: ParseFrame accepted %q", name, line)
		}
	}
}

// TestNoDialectFieldsRemain is the regression that keeps the invented wire
// from returning. It asserts on the SERIALIZED frame, because that is what
// a client sees.
func TestNoDialectFieldsRemain(t *testing.T) {
	encoded, err := json.Marshal(mcp.Frame{JSONRPC: "2.0", Method: mcp.MethodToolsList})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"mcp_method", "mcp_name"} {
		if jsonHasKey(string(encoded), banned) {
			t.Errorf("the frame still carries %q: %s", banned, encoded)
		}
	}
	// And a frame WITHOUT them must now be accepted, which is the half
	// that was broken.
	frame, perr := mcp.ParseFrame([]byte(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	if perr != nil {
		t.Fatalf("a spec-shaped frame was rejected: %v", perr)
	}
	if resp := mcp.NewServer(emptyRegistry()).Dispatch(context.Background(), frame); resp.Error != nil {
		t.Fatalf("a spec-shaped frame was refused at dispatch: %v", resp.Error)
	}
}

// jsonHasKey reports whether doc contains "key" as a JSON key.
func jsonHasKey(doc, key string) bool {
	needle := `"` + key + `"`
	for i := 0; i+len(needle) <= len(doc); i++ {
		if doc[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
