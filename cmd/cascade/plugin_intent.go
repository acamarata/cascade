// Purpose: the intent half of `cascade plugin add <ref>`: a ref that names
// no file is routed as the install intent to the one enabled plugin that
// declares it (cascade-pa's install flow), through the daemon's
// plugin.intent verb when a daemon runs, else through an in-process
// dispatcher over this binary's own builtin registry.
//
// Inputs: the ref as typed, which becomes the flow's {"intent": ref}.
// Outputs: the routed plugin's result, rendered by the output writer; or a
// typed refusal (KindNotFound, KindConflict, the flow's own errors).
// Constraints: explicit invocation only. CASCADE_NO_INPUT=1 refuses before
// any dispatch, so no proposal is ever made that nobody can answer. This
// command grants nothing itself: an install the flow attempts passes the
// flow's own elevation gate.
// SPORT: cmd/cascade plugin add <intent-ref> (ADD) — P1-PLG-07.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// installIntentInput is the install intent's input bytes.
type installIntentInput struct {
	Intent string `json:"intent"`
}

// pluginIntentView renders a routed intent's result.
type pluginIntentView struct {
	Intent string          `json:"intent"`
	Result json.RawMessage `json:"result"`
}

func (v pluginIntentView) String() string {
	head := "cascade plugin add: intent " + strconv.Quote(v.Intent)
	var r struct {
		Resumed *bool `json:"Resumed"`
	}
	switch {
	case json.Unmarshal(v.Result, &r) != nil || r.Resumed == nil:
		return head + " handled: " + string(v.Result)
	case *r.Resumed:
		return head + " handled: the plugin is installed"
	default:
		return head + " handled: no plugin was installed"
	}
}

// runPluginAddIntent routes ref as the install intent.
func runPluginAddIntent(cmd *cobra.Command, deps pluginDeps, ref string) error {
	if pluginNoInput(deps) {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade plugin add: %q names no file, so it is an install intent, and CASCADE_NO_INPUT=1 forbids "+
				"the confirmation it needs; nothing was proposed or installed", ref)
	}
	input, err := json.Marshal(installIntentInput{Intent: ref})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade plugin add: encode intent")
	}
	ctx := cmd.Context()
	var result json.RawMessage
	if pluginDaemonAvailable(ctx) {
		result, err = pluginIntentDaemonCall(ctx, deps, plugins.IntentRPCParams{IntentName: install.IntentName, Input: input})
	} else {
		result, err = dispatchIntentInProcess(ctx, install.IntentName, input)
	}
	if err != nil {
		return err
	}
	return pluginOutputWriter(cmd).Result(pluginIntentView{Intent: ref, Result: result})
}

// pluginIntentDaemonCall dials plugin.intent. The verb is not elevated: the
// install it may lead to is gated inside the flow. Tests replace it to reach
// an in-process handler.
var pluginIntentDaemonCall = func(ctx context.Context, deps pluginDeps, params plugins.IntentRPCParams) (json.RawMessage, error) {
	c, err := pluginClient(deps)
	if err != nil {
		return nil, err
	}
	var out plugins.IntentRPCResult
	if err := c.Do(ctx, plugins.IntentRPCMethod, params, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// dispatchIntentInProcess routes through this binary's builtin registry,
// the same dispatcher the daemon mounts. A manifest Load rejects is not
// indexed, so it can never be routed; the rejection is logged, not fatal.
func dispatchIntentInProcess(ctx context.Context, name string, input []byte) (json.RawMessage, error) {
	reg := &plugins.BuiltinRegistry{}
	if err := reg.Load(); err != nil {
		slog.Default().Warn("plugin intent: builtin registry rejected manifests; they are not routable", "err", err)
	}
	return plugins.NewIntentDispatcher(reg).Dispatch(ctx, name, input)
}
