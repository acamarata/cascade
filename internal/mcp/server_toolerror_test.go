package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose (this file): the 2025-11-25 answer to a tool that failed. It
// sits beside server_test.go, which is at its line budget, and shares its
// resultOf and mustID helpers.

// TestMCPToolErrorIsToolResult: a tool's own failure, whether a business
// error or its arguments failing the handler's validation, reaches the
// model as a tools/call result (isError true, the typed message as text),
// never as a JSON-RPC error object. resultOf fails on any protocol error.
func TestMCPToolErrorIsToolResult(t *testing.T) {
	sealed := cascade.New(cascade.KindConflict, "widget: already sealed")
	badArgs := cascade.New(cascade.KindInvalidInput, `widget: "id" is required`)
	seal := func(_ context.Context, in []byte) ([]byte, error) {
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(in, &args); err != nil || args.ID == "" {
			return nil, badArgs
		}
		return nil, sealed
	}
	tools := mcp.NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, mcp.AllowAllFilter{},
		mcp.CoreRegistration{Tool: mcp.Tool{Name: "widget_seal"}, Grants: []string{"read"}, Handler: seal})
	s := mcp.NewServer(tools)
	for args, want := range map[string]*cascade.Error{`{"id":"w1"}`: sealed, `{}`: badArgs} {
		got := resultOf(t, s.Dispatch(context.Background(), &mcp.Frame{
			JSONRPC: "2.0", Method: mcp.MethodToolsCall, ID: mustID(t, 7),
			Params: json.RawMessage(`{"name":"widget_seal","arguments":` + args + `}`),
		}))
		content, _ := got["content"].([]any)
		if len(content) != 1 || got["isError"] != true {
			t.Fatalf("arguments %s: result = %v, want one content block and isError true", args, got)
		}
		block, _ := content[0].(map[string]any)
		if block["type"] != "text" || block["text"] != want.Error() {
			t.Errorf("arguments %s: content[0] = %v, want a text block %q", args, block, want.Error())
		}
	}
}
