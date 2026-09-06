package hooks

// Purpose: the shell action's policy gate. Every shell action a hook
//
//	fires is routed through the one authorization middleware before it
//	runs, and refused whenever routing does not return an allow.
//
// Inputs: the firing HookConfig, the injected ActionRouter, the injected
//
//	ShellRunner, and the routing subject/capability the dispatcher was
//	built with.
//
// Outputs: a hookOutcome — success, error, ask (dispatch suppressed,
//
//	approval filed by the evaluator) or refused.
//
// Constraints: fail closed. A dispatcher with no router refuses every
//
//	shell action; a routing call that errors refuses; an ask does not
//	dispatch. There is no configuration in which a shell action runs
//	without an allow verdict.
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
// depends on the interface only, exactly as it does for plugin-call and
// agent-note. NO production implementation exists yet anywhere in this
// tree, which is stated here rather than left implicit (Art.1).
type ShellRunner interface {
	// RunShell runs command on behalf of hookID with params. hookID
	// identifies the firing hook; params is the hook's action_params
	// after the egress firewall has substituted them.
	RunShell(ctx context.Context, hookID string, command string, params map[string]string) error
}

// runShellAction routes hook's shell action and dispatches it only on an
// allow. It is called from runAction's ActionTypeShell arm.
func (d *Dispatcher) runShellAction(ctx context.Context, hook HookConfig) hookOutcome {
	if d.router == nil || d.shellRunner == nil {
		return hookOutcome{result: ResultRefused, err: cascade.Newf(cascade.KindPolicyDenied,
			"hooks: shell action %q cannot run: no policy routing is wired (%s)",
			hook.ID, HookActionNotPermittedCode)}
	}
	command := hook.ActionParams[ShellCommandParam]
	if command == "" {
		return hookOutcome{result: ResultRefused, err: cascade.Newf(cascade.KindInvalidInput,
			"hooks: shell action %q has no %q parameter to classify (%s)",
			hook.ID, ShellCommandParam, HookActionNotPermittedCode)}
	}

	verdict, _, err := d.router.RouteAction(ctx, routing.Action{
		Subject:    d.routeSubject,
		Capability: d.shellCapability,
		Verb:       string(EventKindHookFire),
		Command:    command,
		Params:     []byte(paramsHash(hook.ActionParams)),
		Origin:     routing.OriginHook,
		Ref:        hook.ID,
		Summary:    "hook " + hook.ID + " shell action",
	})
	switch verdict {
	case policy.VerdictAllow:
		if err != nil {
			return hookOutcome{result: ResultRefused, err: err}
		}
		return d.dispatchShell(ctx, hook, command)
	case policy.VerdictAsk:
		return hookOutcome{result: ResultAsk, err: cascade.Newf(cascade.KindPolicyDenied,
			"hooks: shell action %q is awaiting approval and was not dispatched (%s)",
			hook.ID, HookActionNotPermittedCode)}
	case policy.VerdictDeny:
		return hookOutcome{result: ResultRefused, err: shellRouteError(hook.ID, err)}
	default:
		return hookOutcome{result: ResultRefused, err: shellRouteError(hook.ID, err)}
	}
}

// shellRouteError returns the refusal for a non-allow routing result,
// preserving the router's own typed error when it gave one and refusing
// anyway when it did not.
func shellRouteError(hookID string, err error) error {
	if err != nil {
		return err
	}
	return cascade.Newf(cascade.KindPolicyDenied,
		"hooks: shell action %q was not authorized (%s)", hookID, HookActionNotPermittedCode)
}

// dispatchShell runs an allowed shell action through the egress firewall
// and the injected runner, on the same terms as every other action type.
func (d *Dispatcher) dispatchShell(ctx context.Context, hook HookConfig, command string) hookOutcome {
	params, ferr := interceptParams(ctx, d.firewall, d.egressToken, hook.ActionParams)
	if ferr != nil {
		return hookOutcome{result: ResultRefused, err: ferr}
	}
	if err := d.shellRunner.RunShell(ctx, hook.ID, command, params); err != nil {
		return hookOutcome{result: ResultError, err: err}
	}
	return hookOutcome{result: ResultSuccess}
}
