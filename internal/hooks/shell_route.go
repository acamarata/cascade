package hooks

// Purpose: the policy gate every action passes before the rehydration seam
//
//	and its runner. Every action type, shell included, is routed through
//	the one authorization middleware with routing.OriginHook and refused
//	whenever routing does not return an allow.
//
// Inputs: the Fire, its post-egress (tagged) params, the injected
//
//	ActionRouter, and the routing subject and per-type capability the
//	dispatcher was built with.
//
// Outputs: nothing on an allow; otherwise a hookOutcome, ask (not run,
//
//	approval filed by the evaluator) or refused.
//
// Constraints: fail closed. A routing call that errors refuses; an ask
//
//	does not dispatch; an allow returned with an error refuses. The routed
//	command and params hash are built from tagged params only, so the
//	router's audit row never sees a credential. Shell never enters the
//	runner table; it runs through ShellRunner (Dispatcher.invoke).
//
// SPORT: internal.hooks.ShellRunner/ADDED, internal.hooks.ActionRouter/ADDED
//
//	(P1-E09-W2-S18-T5).

import (
	"context"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ResultAsk reports that routing returned ask: the action was NOT
// dispatched and an approval was filed for it. It is a distinct outcome
// from ResultRefused, because a refused action is over and an asked one
// is waiting.
const ResultAsk ResultCode = "ask"

// ShellCommandParam is the action_params key a shell hook's command text
// is read from. A shell hook with no such key has no command to classify
// and is refused, never run with an empty command.
const ShellCommandParam = "command"

// ActionRouter is the policy-routing seam. It is satisfied by
// *routing.ActionRouter and declared as an interface so this package
// never constructs one and a test can drive the refusal paths a real
// engine cannot be made to produce on demand.
type ActionRouter interface {
	// RouteAction decides whether action may run.
	RouteAction(ctx context.Context, action routing.Action) (policy.Verdict, policy.Trace, error)
}

// ShellRunner executes a shell action that routing allowed. The
// composition root wires the concrete implementation; this package
// depends on the interface only. NO production implementation exists yet
// anywhere in this tree, which is stated here rather than left implicit.
type ShellRunner interface {
	// RunShell runs command on behalf of hookID with params. command and
	// params are the rehydration seam's output for this fire.
	RunShell(ctx context.Context, hookID string, command string, params map[string]string) error
}

// routeAction asks the router whether fire may run. It returns ok=true
// only on a clean allow; otherwise the outcome to record.
func (d *Dispatcher) routeAction(ctx context.Context, fire Fire, tagged map[string]string) (hookOutcome, bool) {
	hook := fire.Hook
	capability := d.capabilities[hook.ActionType]
	if hook.ActionType == ActionTypeShell {
		capability = d.shellCapability
		if tagged[ShellCommandParam] == "" {
			return hookOutcome{result: ResultRefused, err: cascade.Newf(cascade.KindInvalidInput,
				"hooks: shell action %q has no %q parameter to classify (%s)",
				hook.ID, ShellCommandParam, HookActionNotPermittedCode)}, false
		}
	}
	verdict, _, err := d.router.RouteAction(ctx, routing.Action{
		Subject:    d.routeSubject,
		Capability: capability,
		Verb:       string(EventKindHookFire),
		Command:    routeCommand(hook.ActionType, tagged),
		Params:     []byte(paramsHash(tagged)),
		Origin:     routing.OriginHook,
		Ref:        hook.ID,
		Summary:    "hook " + hook.ID + " " + string(hook.ActionType) + " action",
	})
	switch {
	case verdict == policy.VerdictAllow && err == nil:
		return hookOutcome{}, true
	case verdict == policy.VerdictAsk && err == nil:
		return hookOutcome{result: ResultAsk, err: cascade.Newf(cascade.KindPolicyDenied,
			"hooks: %s action %q is awaiting approval and was not dispatched (%s)",
			hook.ActionType, hook.ID, HookActionNotPermittedCode)}, false
	default:
		return hookOutcome{result: ResultRefused, err: routeRefusal(hook, err)}, false
	}
}

// routeRefusal returns the refusal for a non-allow routing result,
// preserving the router's own typed error when it gave one and refusing
// anyway when it did not.
func routeRefusal(hook HookConfig, err error) error {
	if err != nil {
		return err
	}
	return cascade.Newf(cascade.KindPolicyDenied,
		"hooks: %s action %q was not authorized (%s)", hook.ActionType, hook.ID, HookActionNotPermittedCode)
}
