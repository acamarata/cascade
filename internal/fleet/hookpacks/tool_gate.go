package hookpacks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodToolGate is the RPC method name for the tool gate.
const MethodToolGate = "fleet.sessions.tool_gate"

// ToolGateCapability names the capability this gate provides.
const ToolGateCapability = "tool_gate"

// ToolGateClientDeadline is the client-side timeout for a tool gate call.
const ToolGateClientDeadline = 5 // seconds

// ToolGateHookTimeoutSeconds is the harness-side hook timeout.
const ToolGateHookTimeoutSeconds = 30

// maxToolGateInput is the maximum allowed input size (1 MiB).
const maxToolGateInput = 1 << 20

// ReadOnlyTools is the set of tools that never mutate state.
var ReadOnlyTools = map[string]bool{
	"Read":         true,
	"Glob":         true,
	"Grep":         true,
	"LS":           true,
	"NotebookRead": true,
}

// TaintOnCallTools is the set of inbox MCP tools that taint on call.
var TaintOnCallTools = map[string]bool{
	"cascade_inbox_read": true,
}

// TaintsOnCall reports whether toolName should mark the session untrusted:
// an inbox tool under its own name or under an MCP spelling
// mcp__<server>__<tool> (plugin and user scope).
func TaintsOnCall(toolName string) bool {
	if TaintOnCallTools[toolName] {
		return true
	}
	if !strings.HasPrefix(toolName, "mcp__") {
		return false
	}
	i := strings.LastIndex(toolName, "__")
	return i > len("mcp") && TaintOnCallTools[toolName[i+2:]]
}

// ToolGateParams carries the input to DecideToolGate.
type ToolGateParams struct {
	SessionID     string `json:"session_id"`
	ToolName      string `json:"tool_name"`
	HookEventName string `json:"hook_event_name"`
}

// ToolGateResult carries the output of DecideToolGate.
type ToolGateResult struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// TaintStore is the subset of sessions.Store used by the tool gate.
type TaintStore interface {
	Taint(ctx context.Context, id string) (sessions.TaintState, error)
	MarkUntrusted(ctx context.Context, id, provenanceRef string) error
}

// ToolPolicy decides whether a mutating tool is allowed in a tainted session.
type ToolPolicy interface {
	AllowTool(ctx context.Context, sessionID, toolName string) (bool, error)
}

// DecideToolGate gates a PreToolUse hook event. It fails closed on every
// error path and only consults the policy for a mutating tool in a tainted
// session.
func DecideToolGate(
	ctx context.Context,
	params ToolGateParams,
	harness string,
	store TaintStore,
	policy ToolPolicy,
) (ToolGateResult, error) {
	if params.HookEventName != string(EventPreToolUse) {
		return deny("wrong event"), nil
	}
	if params.SessionID == "" || params.ToolName == "" {
		return deny("empty ids"), nil
	}
	if harness != "claude-code" {
		return deny("harness mismatch"), nil
	}
	if TaintsOnCall(params.ToolName) {
		ref := fmt.Sprintf("mcp:+%s", params.ToolName)
		if err := store.MarkUntrusted(ctx, params.SessionID, ref); err != nil {
			return deny("mark error"), nil
		}
		return allow(), nil
	}
	ts, err := store.Taint(ctx, params.SessionID)
	if err != nil {
		return deny("store error"), nil
	}
	if !ts.Tainted {
		return allow(), nil
	}
	if ReadOnlyTools[params.ToolName] {
		return allow(), nil
	}
	ok, err := policy.AllowTool(ctx, params.SessionID, params.ToolName)
	if err != nil {
		return deny("engine error"), nil
	}
	if !ok {
		return deny("policy denied"), nil
	}
	return allow(), nil
}

func deny(reason string) ToolGateResult {
	return ToolGateResult{Allow: false, Reason: reason}
}

func allow() ToolGateResult {
	return ToolGateResult{Allow: true}
}

// ParseToolGateParams decodes raw JSON bytes into ToolGateParams after
// enforcing the size limit and checking for malformed input.
func ParseToolGateParams(raw []byte) (ToolGateParams, error) {
	if len(raw) > maxToolGateInput {
		return ToolGateParams{}, cascade.New(cascade.KindInvalidInput, "tool_gate: input too large")
	}
	if len(raw) == 0 {
		return ToolGateParams{}, cascade.New(cascade.KindInvalidInput, "tool_gate: empty input")
	}
	var p ToolGateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolGateParams{}, cascade.New(cascade.KindInvalidInput, "tool_gate: bad json")
	}
	return p, nil
}
