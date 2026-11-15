package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// noopHandlers satisfies plugin.BuiltinHandlers for synthetic
// registrations in this file's fail-closed policy tests — the tests never
// call DispatchTool (they assert on List(), which never dispatches), so
// these bodies are never reached in this file.
type noopHandlers struct{}

func (noopHandlers) DispatchTool(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (noopHandlers) DispatchIntent(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}
func (noopHandlers) RunCommand(context.Context, string, []string) error { return nil }

func reg(id string, grants []string, toolName string) plugin.BuiltinRegistration {
	return plugin.BuiltinRegistration{
		Manifest: plugin.Manifest{
			ID: id,
			Provides: plugin.Provides{
				Tools: []plugin.ToolSpec{{Name: toolName, Description: "d"}},
			},
		},
		Handlers: noopHandlers{},
		Grants:   grants,
	}
}

// TestToolRegistry_FailClosedPolicy is hard requirement #2: it asserts the
// exposed set against expectations computed BY HAND in this test (never by
// importing or re-deriving isExposable's own table), so a wrong policy
// table cannot pass by agreeing with itself. Each case names a grants
// value and states, independently, whether a plugin with that grants value
// ought to be MCP-exposable per this ticket's fail-closed rule (only the
// closed, currently-one-member safe set may appear, and it must appear
// alone).
func TestToolRegistry_FailClosedPolicy(t *testing.T) {
	cases := []struct {
		name       string
		grants     []string
		wantExpose bool
	}{
		{"nil grants", nil, false},
		{"empty grants", []string{}, false},
		{"safe read grant", []string{"read"}, true},
		{"unknown single grant", []string{"write"}, false},
		{"unrecognized alongside safe", []string{"read", "admin"}, false},
		{"malformed empty-string grant", []string{""}, false},
		{"duplicate safe grant", []string{"read", "read"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := func() []plugin.BuiltinRegistration {
				return []plugin.BuiltinRegistration{reg("p-"+tc.name, tc.grants, "tool-"+tc.name)}
			}
			r := NewToolRegistry(source, AllowAllFilter{})
			list := r.List()
			exposed := len(list) == 1
			if exposed != tc.wantExpose {
				t.Fatalf("grants=%v: exposed=%v, want %v (list=%v)", tc.grants, exposed, tc.wantExpose, list)
			}
		})
	}
}

// TestToolRegistry_UnknownToolCallFailsClosed proves a filtered-out tool
// cannot be invoked by name even though its manifest declared it — the
// filter is enforced at Call, not merely at List.
func TestToolRegistry_UnknownToolCallFailsClosed(t *testing.T) {
	source := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{reg("p1", []string{"admin"}, "secret-tool")}
	}
	r := NewToolRegistry(source, AllowAllFilter{})
	if _, err := r.Call(context.Background(), "secret-tool", nil); err == nil {
		t.Fatal("Call succeeded for a filtered-out tool, want an error")
	}
}

// TestToolRegistry_List_Deterministic proves sorted, stable output across
// multiple exposed tools.
func TestToolRegistry_List_Deterministic(t *testing.T) {
	source := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{
			reg("p-b", []string{"read"}, "zzz-tool"),
			reg("p-a", []string{"read"}, "aaa-tool"),
		}
	}
	r := NewToolRegistry(source, AllowAllFilter{})
	list := r.List()
	if len(list) != 2 || list[0].Name != "aaa-tool" || list[1].Name != "zzz-tool" {
		t.Fatalf("List() = %+v, want sorted [aaa-tool, zzz-tool]", list)
	}
}

// toolsCall builds a tools/call request for name with empty arguments.
func toolsCall(name string) *Frame {
	return &Frame{JSONRPC: "2.0", Method: MethodToolsCall, ID: json.RawMessage("1"),
		Params: json.RawMessage(`{"name":"` + name + `","arguments":{}}`)}
}

// TestMCPRegistryUnknownTool: an unregistered name is ErrToolUnknown
// itself, asserted by identity and message (errors.Is compares Kind only),
// and Dispatch answers it -32602 "Unknown tool: <name>".
func TestMCPRegistryUnknownTool(t *testing.T) {
	r := NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, AllowAllFilter{})
	_, err := r.Call(context.Background(), "no_such_tool", nil)
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce != ErrToolUnknown || err.Error() != "not-found: mcp: unknown tool" {
		t.Fatalf("Call(unknown) = %v, want ErrToolUnknown itself", err)
	}
	resp := NewServer(r).Dispatch(context.Background(), toolsCall("no_such_tool"))
	if resp == nil || resp.Result != nil || resp.Error == nil || resp.Error.Code != -32602 ||
		resp.Error.Message != "Unknown tool: no_such_tool" {
		t.Fatalf("Dispatch(unknown) = %+v, want -32602 \"Unknown tool: no_such_tool\"", resp)
	}
}

// TestMCPUnknownToolInvalidParams: a withheld tool gets the same -32602
// answer as an absent one, while a handler's own not-found error (same
// Kind as ErrToolUnknown) stays a tool result.
func TestMCPUnknownToolInvalidParams(t *testing.T) {
	missing := cascade.New(cascade.KindNotFound, "widget w1 not found")
	lookup := CoreRegistration{Tool: Tool{Name: "widget_get"}, Grants: []string{"read"},
		Handler: func(context.Context, []byte) ([]byte, error) { return nil, missing }}
	source := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{reg("p1", []string{"admin"}, "secret-tool")}
	}
	s := NewServer(NewToolRegistry(source, AllowAllFilter{}, lookup))
	resp := s.Dispatch(context.Background(), toolsCall("secret-tool"))
	if resp == nil || resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Message != "Unknown tool: secret-tool" {
		t.Fatalf("withheld tool = %+v, want -32602 \"Unknown tool: secret-tool\"", resp)
	}
	resp = s.Dispatch(context.Background(), toolsCall("widget_get"))
	result, ok := resp.Result.(toolCallResult)
	if resp.Error != nil || !ok || !result.IsError || len(result.Content) != 1 || result.Content[0].Text != missing.Error() {
		t.Fatalf("handler not-found = %+v, want an isError result carrying %q", resp, missing.Error())
	}
}

// TestMCPPingAnswered: the receiver MUST answer ping, with an empty result.
func TestMCPPingAnswered(t *testing.T) {
	s := NewServer(NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, AllowAllFilter{}))
	raw, err := json.Marshal(s.Dispatch(context.Background(), &Frame{JSONRPC: "2.0", Method: "ping", ID: json.RawMessage("9")}))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"jsonrpc":"2.0","id":9,"result":{}}` {
		t.Fatalf("ping = %s, want an empty result", raw)
	}
}

// TestMCPCancelledNotificationIsSilent: notifications/cancelled for an
// answered id yields no frame and changes no later answer.
func TestMCPCancelledNotificationIsSilent(t *testing.T) {
	source := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{reg("p-a", []string{"read"}, "aaa-tool")}
	}
	s := NewServer(NewToolRegistry(source, AllowAllFilter{}))
	list := &Frame{JSONRPC: "2.0", Method: MethodToolsList, ID: json.RawMessage("1")}
	before, err := json.Marshal(s.Dispatch(context.Background(), list))
	if err != nil {
		t.Fatal(err)
	}
	cancel := &Frame{JSONRPC: "2.0", Method: "notifications/cancelled",
		Params: json.RawMessage(`{"requestId":1,"reason":"user"}`)}
	if resp := s.Dispatch(context.Background(), cancel); resp != nil {
		t.Fatalf("notifications/cancelled answered %+v, want silence", resp)
	}
	after, err := json.Marshal(s.Dispatch(context.Background(), list))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || len(before) == 0 {
		t.Fatalf("answer changed after a cancellation:\n %s\n %s", before, after)
	}
}
