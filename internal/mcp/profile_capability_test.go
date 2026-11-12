package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

type toolCheckerFunc func(context.Context, string) error

func (f toolCheckerFunc) CheckTool(ctx context.Context, tool string) error {
	return f(ctx, tool)
}

func capabilityServer(tool string, calls *int) *Server {
	registry := NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, AllowAllFilter{},
		CoreRegistration{Tool: Tool{Name: tool}, Grants: []string{"read"},
			Handler: func(context.Context, []byte) ([]byte, error) {
				*calls++
				return []byte(`{}`), nil
			}})
	return NewServer(registry)
}

func capabilityCall(s *Server, tool string) *Response {
	params, _ := json.Marshal(toolCallParams{Name: tool, Arguments: json.RawMessage(`{}`)})
	return s.Dispatch(context.Background(), &Frame{
		JSONRPC: "2.0", Method: MethodToolsCall, ID: json.RawMessage(`1`), Params: params,
	})
}

func assertCapabilityDenied(t *testing.T, resp *Response) {
	t.Helper()
	if resp == nil || resp.Error != nil {
		t.Fatalf("want tool error, got %+v", resp)
	}
	result, ok := resp.Result.(toolCallResult)
	if !ok || !result.IsError || len(result.Content) != 1 ||
		result.Content[0].Text != cascade.ErrPolicyDenied.Error() {
		t.Fatalf("want policy_denied tool error, got %+v", resp.Result)
	}
}

func TestToolCapabilityCheckerFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"deny", cascade.ErrPolicyDenied},
		{"ask", errors.New("approval required")},
		{"RPC error", errors.New("RPC failed")},
		{"unreachable daemon", cascade.ErrUnavailable},
		{"nil checker", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range CompactWriteToolNames {
				calls, checks := 0, 0
				var checker ToolCapabilityChecker
				if tc.name != "nil checker" {
					checker = toolCheckerFunc(func(context.Context, string) error { checks++; return tc.err })
				}
				s := capabilityServer(tool, &calls).WithToolChecker(ProfileCompactWrite, checker)
				assertCapabilityDenied(t, capabilityCall(s, tool))
				if err := s.checkTool(context.Background(), tool); err != cascade.ErrPolicyDenied || err.Error() != "policy-denied" {
					t.Fatalf("error = %v, want policy denial sentinel by identity and message", err)
				}
				if calls != 0 || (checker != nil && checks != 2) {
					t.Fatalf("handler calls = %d, checks = %d", calls, checks)
				}
			}
		})
	}
	t.Run("other profiles bypass checker", testOtherProfilesBypassChecker)
}

func testOtherProfilesBypassChecker(t *testing.T) {
	for _, profile := range []ToolProfile{ProfileCompact, ProfileFull} {
		calls := 0
		checker := toolCheckerFunc(func(context.Context, string) error {
			t.Fatal("checker consulted outside compact-write")
			return cascade.ErrPolicyDenied
		})
		s := capabilityServer(CompactWriteToolNames[0], &calls).WithToolChecker(profile, checker)
		resp := capabilityCall(s, CompactWriteToolNames[0])
		if resp.Error != nil || calls != 1 {
			t.Fatalf("profile %q: response = %+v, calls = %d", profile, resp, calls)
		}
	}
}

func TestCompactWriteToolCapabilityDenied(t *testing.T) {
	calls := 0
	checker := toolCheckerFunc(func(_ context.Context, tool string) error {
		if ToolCapability(tool) == "mcp.tool.cascade_memory_remember" {
			return cascade.ErrPolicyDenied
		}
		return nil
	})
	s := capabilityServer("cascade_memory_remember", &calls).WithToolChecker(ProfileCompactWrite, checker)
	assertCapabilityDenied(t, capabilityCall(s, "cascade_memory_remember"))
	if calls != 0 {
		t.Fatal("denied handler ran")
	}
}

func TestToolCapabilityCheckerRunsOnEveryCall(t *testing.T) {
	calls, checks := 0, 0
	tool := CompactWriteToolNames[0]
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "caller")
	checker := toolCheckerFunc(func(got context.Context, name string) error {
		checks++
		if got.Value(contextKey{}) != "caller" || name != tool {
			t.Fatalf("checker lost caller context or tool name: %q", name)
		}
		if checks > 1 {
			return cascade.ErrPolicyDenied
		}
		return nil
	})
	s := capabilityServer(tool, &calls).WithToolChecker(ProfileCompactWrite, checker)
	params, _ := json.Marshal(toolCallParams{Name: tool})
	frame := &Frame{JSONRPC: "2.0", Method: MethodToolsCall, ID: json.RawMessage(`1`), Params: params}
	first := s.Dispatch(ctx, frame)
	if first.Error != nil || first.Result.(toolCallResult).IsError || calls != 1 {
		t.Fatalf("allowed call failed: %+v, handler calls = %d", first, calls)
	}
	assertCapabilityDenied(t, s.Dispatch(ctx, frame))
	if checks != 2 || calls != 1 {
		t.Fatalf("checks = %d, handler calls = %d; want 2, 1", checks, calls)
	}
}

func TestToolCapabilityCheckerBypassesReadTools(t *testing.T) {
	for _, tool := range CompactToolNames {
		calls := 0
		checker := toolCheckerFunc(func(context.Context, string) error {
			t.Fatal("checker consulted for read tool")
			return cascade.ErrPolicyDenied
		})
		for _, c := range []ToolCapabilityChecker{nil, checker} {
			s := capabilityServer(tool, &calls).WithToolChecker(ProfileCompactWrite, c)
			resp := capabilityCall(s, tool)
			if resp.Error != nil || resp.Result.(toolCallResult).IsError {
				t.Fatalf("read tool %s failed: %+v", tool, resp)
			}
		}
		if calls != 2 {
			t.Fatalf("read tool %s: handler calls = %d, want 2", tool, calls)
		}
	}
}

func TestToolCapabilityCheckerUsesAdditiveMemberSet(t *testing.T) {
	previous := CompactWriteToolNames
	t.Cleanup(func() { CompactWriteToolNames = previous })
	CompactWriteToolNames = append(append([]string(nil), previous...), "scratch_write")
	calls := 0
	s := capabilityServer("scratch_write", &calls).WithToolChecker(ProfileCompactWrite, nil)
	assertCapabilityDenied(t, capabilityCall(s, "scratch_write"))
	if calls != 0 {
		t.Fatal("appended compact-write tool bypassed checker")
	}
}
