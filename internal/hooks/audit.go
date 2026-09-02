package hooks

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
)

// Purpose: task 5 — the deferred audit emit. Every fire outcome
//
//	dispatchHook produces (success, error, panic, timeout, refused) is
//	turned into a HookFire and published to the event bus here; this is
//	the single call site that does so, so "every fire is audited" is
//	enforced by construction (there is exactly one path from an outcome
//	to a publish) rather than by convention.
//
// Inputs: a HookConfig and the hookOutcome dispatchHook computed for it.
//
// Outputs: one events.Bus.Publish call per invocation, namespace
//
//	d.auditNamespace, kind EventKindHookFire, payload a JSON-encoded
//	HookFire.
//
// Constraints: SECURITY — audit.go is where a hook's own action_params
//
//	could otherwise leak into the audit trail, because ErrMsg is
//	free-form text sourced from whatever error runAction's inner call
//	returned (a PluginDispatcher/NoteWriter implementation the caller
//	does not control the wording of). This package cannot inspect
//	arbitrary downstream error text for every possible secret shape, but
//	it CAN check the one thing it knows for certain might appear
//	verbatim in that text: the hook's OWN action_params values. redactErr
//	therefore checks each action_params value against
//	runtime.LooksLikeSecret (the same heuristic `cascade config
//	set`/`edit` already screens with, internal/runtime/
//	config_write_secrets.go) and, for every value that looks
//	secret-shaped, replaces every literal occurrence of it inside ErrMsg
//	with "[REDACTED]" before publication — belt-and-braces alongside the
//	structural fact that HookFire never carries the raw ActionParams map
//	at all, only ParamsHash. This does not (and cannot) catch a secret
//	value that does NOT look secret-shaped, or a secret introduced by the
//	downstream implementation from some source other than this hook's own
//	params — stated plainly, not silently assumed complete.
//
// SPORT: internal.hooks.HookFire/ADDED (audit path) (P1-E03-W1-S05-T1).

// emitAudit builds and publishes the HookFire record for one dispatch
// attempt. Publish errors (a Store failure, or the bus having been
// closed) are deliberately swallowed here rather than propagated: audit.go
// is itself the last stop after a hook fire, called from dispatchHook
// which returns nothing — there is no caller left to hand a publish
// failure to, and panicking the dispatch loop over an audit-plumbing
// failure would be strictly worse than losing one audit record. A bus
// Publish failure of this kind is itself the kind of operational
// condition internal/events' own KindUnavailable error exists to
// surface through its OTHER callers (e.g. a health check); it is not
// silently invisible system-wide, only invisible to this one caller.
func (d *Dispatcher) emitAudit(hook HookConfig, outcome hookOutcome) {
	fire := HookFire{
		HookID:     hook.ID,
		Trigger:    hook.Trigger,
		ActionType: hook.ActionType,
		ParamsHash: paramsHash(hook.ActionParams),
		ResultCode: outcome.result,
		Ts:         d.clock.Now(),
	}
	if outcome.err != nil {
		fire.ErrMsg = redactSecrets(outcome.err.Error(), hook.ActionParams)
	}

	payload, err := json.Marshal(fire)
	if err != nil {
		// A HookFire value is always JSON-marshalable (string/time
		// fields only) — this branch exists only so a future field
		// addition that breaks that invariant fails loudly in CI rather
		// than silently dropping every audit record.
		payload = []byte(`{"marshal_error":true}`)
	}

	_, _ = d.bus.Publish(context.Background(), d.auditNamespace, EventKindHookFire, hook.ID, payload)
}

// redactSecrets returns msg with every literal occurrence of a
// secret-shaped value from params replaced by "[REDACTED]". See this
// file's package comment for what this does and does not guarantee.
func redactSecrets(msg string, params map[string]string) string {
	for _, v := range params {
		if v == "" {
			continue
		}
		if bad, _ := runtime.LooksLikeSecret(v); bad {
			msg = strings.ReplaceAll(msg, v, "[REDACTED]")
		}
	}
	return msg
}
