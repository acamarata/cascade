// Purpose: install the tool gate with the shared renderer and bound gate hooks.

package hookpacks

import "time"

// CompletionGateHookTimeoutSeconds bounds the TaskCompleted and Stop hooks:
// the client budget in seconds + 20, at least 30 (R22 M-3).
const CompletionGateHookTimeoutSeconds = max(30, int((defaultCompletionTimeout+completionHookClientSlack)/time.Second)+20)

// ToolGatePack is the "tool-gate" pack: one PreToolUse descriptor that fails closed.
func ToolGatePack() HookPack {
	return HookPack{
		Name: "tool-gate",
		Descriptors: []HookDescriptor{{
			EventType:       EventPreToolUse,
			Matcher:         "",
			CommandTemplate: binaryPlaceholder + " fleet tool-gate --socket '" + socketPlaceholder + "' || exit 2",
			TimeoutSeconds:  ToolGateHookTimeoutSeconds,
		}},
	}
}
