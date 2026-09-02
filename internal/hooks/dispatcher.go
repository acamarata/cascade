package hooks

import (
	"context"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// Purpose: the event-bus subscriber and action dispatcher — task 3
//
//	(subscribe, match, enqueue) and task 4 (dispatch plugin-call/
//	agent-note, defense-in-depth shell/unknown refusal).
//
// Inputs: a *Registry, a *events.Bus subscription, an injected
//
//	PluginDispatcher/NoteWriter pair, a runtime.Clock, and an action
//	timeout.
//
// Outputs: exactly one HookFire audit publish (via audit.go) per matched
//
//	hook per event, covering success/error/panic/timeout/refused.
//
// Constraints: dispatchHook must never let one hook's action block the
//
//	dispatch loop past the configured timeout, even if that action
//	ignores context cancellation and blocks forever — see dispatchHook's
//	comment for the exact mechanism. Non-matching events, and the
//	package's own EventKindHookFire audit events, are discarded silently
//	(no audit, no dispatch).
//
// SPORT: internal.hooks.Dispatcher/ADDED (P1-E03-W1-S05-T1).

// Dispatcher subscribes to a namespace on an events.Bus, matches incoming
// events against a Registry's hooks by exact trigger string, and
// dispatches each match's action. The zero value is not usable; construct
// with NewDispatcher.
type Dispatcher struct {
	registry         *Registry
	bus              *events.Bus
	clock            runtime.Clock
	pluginDispatcher PluginDispatcher
	noteWriter       NoteWriter
	actionTimeout    time.Duration
	triggerNamespace string
	auditNamespace   string
	cursorName       string
	subscribeBuffer  int
}

// DispatcherConfig configures NewDispatcher. Every field is required
// (Art.1: this package invents no numeric or namespace defaults — the
// composition root, which knows the real config, supplies them all).
type DispatcherConfig struct {
	Registry         *Registry
	Bus              *events.Bus
	Clock            runtime.Clock
	PluginDispatcher PluginDispatcher
	NoteWriter       NoteWriter
	ActionTimeout    time.Duration
	TriggerNamespace string
	AuditNamespace   string
	CursorName       string
	SubscribeBuffer  int
}

// NewDispatcher validates cfg and returns a ready-to-use Dispatcher.
func NewDispatcher(cfg DispatcherConfig) (*Dispatcher, error) {
	switch {
	case cfg.Registry == nil:
		return nil, fmt.Errorf("hooks: dispatcher: Registry is required")
	case cfg.Bus == nil:
		return nil, fmt.Errorf("hooks: dispatcher: Bus is required")
	case cfg.Clock == nil:
		return nil, fmt.Errorf("hooks: dispatcher: Clock is required")
	case cfg.PluginDispatcher == nil:
		return nil, fmt.Errorf("hooks: dispatcher: PluginDispatcher is required")
	case cfg.NoteWriter == nil:
		return nil, fmt.Errorf("hooks: dispatcher: NoteWriter is required")
	case cfg.ActionTimeout <= 0:
		return nil, fmt.Errorf("hooks: dispatcher: ActionTimeout must be positive")
	case cfg.TriggerNamespace == "":
		return nil, fmt.Errorf("hooks: dispatcher: TriggerNamespace is required")
	case cfg.AuditNamespace == "":
		return nil, fmt.Errorf("hooks: dispatcher: AuditNamespace is required")
	case cfg.CursorName == "":
		return nil, fmt.Errorf("hooks: dispatcher: CursorName is required")
	case cfg.SubscribeBuffer <= 0:
		return nil, fmt.Errorf("hooks: dispatcher: SubscribeBuffer must be positive")
	}
	return &Dispatcher{
		registry:         cfg.Registry,
		bus:              cfg.Bus,
		clock:            cfg.Clock,
		pluginDispatcher: cfg.PluginDispatcher,
		noteWriter:       cfg.NoteWriter,
		actionTimeout:    cfg.ActionTimeout,
		triggerNamespace: cfg.TriggerNamespace,
		auditNamespace:   cfg.AuditNamespace,
		cursorName:       cfg.CursorName,
		subscribeBuffer:  cfg.SubscribeBuffer,
	}, nil
}

// Run subscribes to the dispatcher's trigger namespace/cursor and
// processes events until ctx is canceled or the subscription reports a
// fatal error. Run is intended to be called once, from its own
// goroutine, by the composition root.
func (d *Dispatcher) Run(ctx context.Context) error {
	sub, err := d.bus.Subscribe(ctx, d.triggerNamespace, d.cursorName, d.subscribeBuffer)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Errs:
			return err
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			d.handleEvent(ctx, ev)
		}
	}
}

// handleEvent matches ev against the registry and dispatches every match.
// A non-matching event, and ev.Kind == EventKindHookFire (this package's
// own audit trail — the direct-self-loop guard described in doc.go), are
// both discarded silently: no dispatch, no audit event.
func (d *Dispatcher) handleEvent(ctx context.Context, ev events.Event) {
	if ev.Kind == EventKindHookFire {
		return
	}
	matches := d.registry.MatchTriggers(string(ev.Kind))
	for _, hook := range matches {
		d.dispatchHook(ctx, hook)
	}
}

// hookOutcome is runAction's return shape, folded into a HookFire by
// dispatchHook.
type hookOutcome struct {
	result ResultCode
	err    error
}

// dispatchHook runs hook's action bounded by the dispatcher's
// actionTimeout and emits exactly one HookFire audit record for the
// attempt, then returns.
//
// Bounding mechanism (this is the "must not hang the system" guarantee):
// runAction executes in its own goroutine and sends its outcome on
// resultCh, which is BUFFERED (capacity 1) so that send can never block —
// even if nobody is listening by the time it happens. dispatchHook itself
// waits on a select between that channel and ctx's deadline (derived from
// actionTimeout). If the action's own goroutine is still blocked inside
// PluginDispatcher/NoteWriter when the deadline fires — including a hook
// that ignores ctx entirely and blocks forever, e.g. `select {}` — the
// select's ctx.Done() branch wins, dispatchHook synthesizes a
// ResultTimeout outcome itself, audits it, and RETURNS immediately: the
// caller (handleEvent's loop, and therefore Run's whole event loop) is
// never blocked longer than actionTimeout by any single hook, regardless
// of what that hook's action actually does. The abandoned goroutine is
// not and cannot be killed (Go has no such primitive); if the action
// later does return, its outcome lands in resultCh with nobody left to
// read it and is silently discarded — a bounded, accepted goroutine leak
// per fire, never a second audit emission for the same fire (audit
// emission happens exactly once, in this function, not in the goroutine).
func (d *Dispatcher) dispatchHook(parentCtx context.Context, hook HookConfig) {
	ctx, cancel := context.WithTimeout(parentCtx, d.actionTimeout)
	defer cancel()

	resultCh := make(chan hookOutcome, 1)
	go func() {
		resultCh <- d.runAction(ctx, hook)
	}()

	var outcome hookOutcome
	select {
	case outcome = <-resultCh:
	case <-ctx.Done():
		outcome = hookOutcome{result: ResultTimeout, err: fmt.Errorf("hooks: action timed out after %s", d.actionTimeout)}
	}

	d.emitAudit(hook, outcome)
}

// runAction performs exactly one hook's inner action call, recovering
// from a panic in that call (or in this function's own switch — belt and
// braces) so a panicking hook can never crash Run's goroutine, let alone
// the process. It is the SECOND, defense-in-depth action-type check
// (task 4): even a HookConfig whose ActionType somehow reached this point
// as shell or unknown — Registry.Register should already have refused it
// — is refused here too, before either injected interface is ever
// called.
func (d *Dispatcher) runAction(ctx context.Context, hook HookConfig) (outcome hookOutcome) {
	defer func() {
		if r := recover(); r != nil {
			outcome = hookOutcome{result: ResultPanic, err: fmt.Errorf("hooks: panic in action: %v", r)}
		}
	}()

	switch hook.ActionType {
	case ActionTypePluginCall:
		if err := d.pluginDispatcher.DispatchPluginCall(ctx, hook.ID, hook.ActionParams); err != nil {
			return hookOutcome{result: ResultError, err: err}
		}
		return hookOutcome{result: ResultSuccess}
	case ActionTypeAgentNote:
		if err := d.noteWriter.WriteAgentNote(ctx, hook.ID, hook.ActionParams); err != nil {
			return hookOutcome{result: ResultError, err: err}
		}
		return hookOutcome{result: ResultSuccess}
	case ActionTypeShell:
		// Recognised but permanently refused at W1 — see hooks.go's
		// ActionTypeShell doc. Falls through to the identical refusal the
		// default case gives any OTHER unrecognised type; kept as its own
		// switch arm (rather than folding into default) purely so the lint
		// wall's exhaustive check proves every declared ActionType member
		// was considered here, not just the two permitted ones.
		return hookOutcome{result: ResultRefused, err: newActionNotPermittedError(hook.ActionType)}
	default:
		return hookOutcome{result: ResultRefused, err: newActionNotPermittedError(hook.ActionType)}
	}
}
