package mcp

import (
	"context"
	"testing"

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
			r := NewToolRegistry(source)
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
	r := NewToolRegistry(source)
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
	r := NewToolRegistry(source)
	list := r.List()
	if len(list) != 2 || list[0].Name != "aaa-tool" || list[1].Name != "zzz-tool" {
		t.Fatalf("List() = %+v, want sorted [aaa-tool, zzz-tool]", list)
	}
}
