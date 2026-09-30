package hooks

import (
	"context"

	"github.com/acamarata/cascade/internal/events"
)

// Purpose: the runner table's element type and the two P1 runners, the
//
//	Fire identity every runner and the rehydration seam receive, and the
//	RehydrateFunc seam signature.
//
// Inputs: a PluginDispatcher or NoteWriter from the composition root.
//
// Outputs: ActionRunner values the dispatcher's Runners map is keyed with.
//
// Constraints: shell never has a runner here; it runs only through
//
//	ShellRunner (shell_route.go). The rehydration seam has no default and
//	no no-op form in this package: a dispatcher without one is refused at
//	construction.

// Fire identifies one dispatch attempt: the hook, the event that matched
// it, and the event's causal chain. Hook.ActionParams is always nil on a
// Fire: a runner gets params only through the rehydration seam's output,
// never the raw configured values.
type Fire struct {
	Hook      HookConfig
	Namespace string
	EventSeq  uint64
	Chain     events.Cause
}

// ActionRunner runs one action type. params are the seam's rehydrated
// params for this fire.
type ActionRunner interface {
	RunAction(ctx context.Context, fire Fire, params map[string]string) error
}

// RehydrateFunc is the dispatch-time seam that turns the post-egress
// (tagged) params into the params the runner receives. It runs after the
// egress pass and the routing verdict and before the runner, once per
// fire. zero releases whatever plaintext the seam produced; the dispatcher
// calls it exactly once on every path, including runner error, panic and
// timeout. An error refuses the fire with ResultRehydrate.
type RehydrateFunc func(ctx context.Context, fire Fire, tagged map[string]string) (plain map[string]string, zero func(), err error)

// PluginParamName and PluginToolParam are the action_params keys a
// plugin-call names its plugin and tool with; NoteNameParam names an
// agent note. They build the routed command text.
const (
	PluginParamName = "plugin"
	PluginToolParam = "tool"
	NoteNameParam   = "note"
)

// pluginCallRunner adapts a PluginDispatcher to the runner table.
type pluginCallRunner struct{ pd PluginDispatcher }

func (r pluginCallRunner) RunAction(ctx context.Context, fire Fire, params map[string]string) error {
	return r.pd.DispatchPluginCall(ctx, fire.Hook.ID, params)
}

// PluginCallRunner returns the plugin-call runner over pd, or nil for a nil
// pd so NewDispatcher refuses the entry instead of panicking at a fire.
func PluginCallRunner(pd PluginDispatcher) ActionRunner {
	if pd == nil {
		return nil
	}
	return pluginCallRunner{pd: pd}
}

// agentNoteRunner adapts a NoteWriter to the runner table.
type agentNoteRunner struct{ nw NoteWriter }

func (r agentNoteRunner) RunAction(ctx context.Context, fire Fire, params map[string]string) error {
	return r.nw.WriteAgentNote(ctx, fire.Hook.ID, params)
}

// AgentNoteRunner returns the agent-note runner over nw, or nil for a nil
// nw so NewDispatcher refuses the entry.
func AgentNoteRunner(nw NoteWriter) ActionRunner {
	if nw == nil {
		return nil
	}
	return agentNoteRunner{nw: nw}
}

// routeCommand is the command text the policy engine classifies for an
// action, built from the tagged params so a credential never reaches the
// router's audit row.
func routeCommand(t ActionType, tagged map[string]string) string {
	switch t {
	case ActionTypePluginCall:
		return string(t) + " " + tagged[PluginParamName] + "." + tagged[PluginToolParam]
	case ActionTypeAgentNote:
		return string(t) + " " + tagged[NoteNameParam]
	case ActionTypeShell:
		return tagged[ShellCommandParam]
	default:
		return string(t)
	}
}
