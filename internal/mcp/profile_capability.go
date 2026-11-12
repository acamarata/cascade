package mcp

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ToolCapability returns the policy capability for an MCP tool name.
func ToolCapability(tool string) string { return "mcp.tool." + tool }

// ToolCapabilityChecker checks a tool name at call time. A nil error allows
// the call; every error, including a request for approval, refuses it.
type ToolCapabilityChecker interface {
	CheckTool(ctx context.Context, tool string) error
}

// WithToolChecker configures call-time checks for compact-write tools.
// A missing checker refuses those calls. Compact and full bypass checks.
// Configure the server before dispatching concurrent requests.
func (s *Server) WithToolChecker(p ToolProfile, c ToolCapabilityChecker) *Server {
	s.toolProfile = ParseToolProfile(string(p))
	s.toolChecker = c
	return s
}

func (s *Server) checkTool(ctx context.Context, tool string) error {
	if s.toolProfile != ProfileCompactWrite {
		return nil
	}
	for _, name := range CompactWriteToolNames {
		if tool != name {
			continue
		}
		if s.toolChecker == nil || s.toolChecker.CheckTool(ctx, tool) != nil {
			return cascade.ErrPolicyDenied
		}
		return nil
	}
	return nil
}
