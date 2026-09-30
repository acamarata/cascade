package hooks

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the action dispatcher. For every matched hook: runnable check,
//
//	budget, egress pass, policy routing, rehydration seam, runner, scrub,
//	zero, and exactly one HookFire.
//
// Inputs: a *Registry, a *events.Bus, the runner table, the rehydration
//
//	seam, the policy router, a runtime.Clock and an action timeout.
//
// Outputs: exactly one HookFire per matched hook per event (audit.go).
//
// Constraints: no action type runs without an allow verdict; the seam runs
//
//	after egress and routing and before the runner; one hook's action never
//	blocks the loop past ActionTimeout, even when it ignores its context,
//	and a timed-out fire starts no later stage.
//	Subscriptions, Run and Swap live in subscriptions.go.

// Dispatcher matches bus events against a Registry's hooks and dispatches
// each match. The zero value is not usable; construct with NewDispatcher.
type Dispatcher struct {
	bus             *events.Bus
	clock           runtime.Clock
	firewall        Interceptor
	egressToken     egress.Capability
	runners         map[ActionType]ActionRunner
	capabilities    map[ActionType]string
	rehydrate       RehydrateFunc
	router          ActionRouter
	shellRunner     ShellRunner
	routeSubject    policy.Subject
	shellCapability string
	state           provider.Store
	actionTimeout   time.Duration
	auditNamespace  string
	cursorPrefix    string
	subscribeBuffer int

	mu       sync.RWMutex
	registry *Registry

	swapMu  sync.Mutex
	running bool
	runCtx  context.Context //nolint:containedctx // the live Run's context, handed to namespace consumers Swap starts
	subs    map[string]*nsSub
	errs    chan error

	budget *budget
	fires  fireLog
}

// DispatcherConfig configures NewDispatcher. Every field is required
// except ShellRunner and ShellCapability, which come as a pair: without
// them shell is unrunnable. This package invents no defaults.
type DispatcherConfig struct {
	Registry    *Registry
	Bus         *events.Bus
	Clock       runtime.Clock
	Egress      Interceptor
	EgressToken egress.Capability
	// Runners is the runner table: one ActionRunner per runnable
	// non-shell action type. An ActionTypeShell key is refused.
	Runners map[ActionType]ActionRunner
	// ActionCapabilities names the registered policy capability each
	// Runners key is routed under; a missing entry is refused.
	ActionCapabilities map[ActionType]string
	// Rehydrate is the dispatch-time rehydration seam. Required.
	Rehydrate RehydrateFunc
	// Router routes every action; RouteSubject is the principal it is
	// evaluated as. Both required.
	Router          ActionRouter
	RouteSubject    policy.Subject
	ShellRunner     ShellRunner
	ShellCapability string
	// State persists the configured namespace set (subscriptions.go).
	State           provider.Store
	ActionTimeout   time.Duration
	AuditNamespace  string
	CursorPrefix    string
	SubscribeBuffer int
}

// NewDispatcher validates cfg and returns a ready-to-use Dispatcher.
func NewDispatcher(cfg DispatcherConfig) (*Dispatcher, error) {
	if err := validateDispatcherConfig(cfg); err != nil {
		return nil, err
	}
	d := &Dispatcher{
		bus: cfg.Bus, clock: cfg.Clock, firewall: cfg.Egress, egressToken: cfg.EgressToken,
		runners: make(map[ActionType]ActionRunner, len(cfg.Runners)), capabilities: make(map[ActionType]string),
		rehydrate: cfg.Rehydrate, router: cfg.Router, shellRunner: cfg.ShellRunner,
		routeSubject: cfg.RouteSubject, shellCapability: cfg.ShellCapability, state: cfg.State,
		actionTimeout: cfg.ActionTimeout, auditNamespace: cfg.AuditNamespace,
		cursorPrefix: cfg.CursorPrefix, subscribeBuffer: cfg.SubscribeBuffer,
		registry: cfg.Registry, budget: newBudget(),
	}
	for t, r := range cfg.Runners {
		d.runners[t] = r
		d.capabilities[t] = cfg.ActionCapabilities[t]
	}
	return d, nil
}

// validateDispatcherConfig refuses a config missing anything it needs.
func validateDispatcherConfig(cfg DispatcherConfig) error {
	checks := []struct {
		bad  bool
		what string
	}{
		{cfg.Registry == nil, "Registry is required"},
		{cfg.Bus == nil, "Bus is required"},
		{cfg.Clock == nil, "Clock is required"},
		{cfg.Egress == nil, "Egress is required"},
		{cfg.EgressToken.Class() != egress.EgressClassHook, "EgressToken must be the hook egress capability"},
		{len(cfg.Runners) == 0, "Runners must not be empty"},
		{cfg.Rehydrate == nil, "Rehydrate is required"},
		{cfg.Router == nil, "Router is required: every action is routed"},
		{cfg.RouteSubject.Validate() != nil, "RouteSubject must name a subject"},
		{(cfg.ShellRunner == nil) != (cfg.ShellCapability == ""), "ShellRunner and ShellCapability come as a pair"},
		{cfg.State == nil, "State is required"},
		{cfg.ActionTimeout <= 0, "ActionTimeout must be positive"},
		{cfg.AuditNamespace != AuditNamespace, "AuditNamespace must be " + AuditNamespace},
		{cfg.CursorPrefix == "", "CursorPrefix is required"},
		{cfg.SubscribeBuffer <= 0, "SubscribeBuffer must be positive"},
	}
	for _, c := range checks {
		if c.bad {
			return cascade.New(cascade.KindInvalidInput, "hooks: dispatcher: "+c.what)
		}
	}
	return validateRunnerTable(cfg.Runners, cfg.ActionCapabilities)
}

// validateRunnerTable refuses a shell entry, a nil runner, a runner with no
// capability, and a capability with no runner.
func validateRunnerTable(runners map[ActionType]ActionRunner, caps map[ActionType]string) error {
	for t, r := range runners {
		switch {
		case t == ActionTypeShell:
			return cascade.New(cascade.KindInvalidInput, "hooks: dispatcher: shell never enters the runner table")
		case r == nil:
			return cascade.Newf(cascade.KindInvalidInput, "hooks: dispatcher: runner for %q is nil", t)
		case caps[t] == "":
			return cascade.Newf(cascade.KindInvalidInput, "hooks: dispatcher: no capability for %q", t)
		}
	}
	for t := range caps {
		if _, ok := runners[t]; !ok {
			return cascade.Newf(cascade.KindInvalidInput, "hooks: dispatcher: capability for %q has no runner", t)
		}
	}
	return nil
}

// Runnable reports whether t can run on this dispatcher: a runner-table
// key, or shell only when a ShellRunner and its capability are wired.
func (d *Dispatcher) Runnable(t ActionType) bool {
	if t == ActionTypeShell {
		return d.shellRunner != nil && d.shellCapability != ""
	}
	_, ok := d.runners[t]
	return ok
}

// currentRegistry returns the registry in force.
func (d *Dispatcher) currentRegistry() *Registry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.registry
}

// handleEvent dispatches every hook on ns matching ev. The package's own
// audit kind never matches (defense-in-depth; Register refuses it too).
func (d *Dispatcher) handleEvent(ctx context.Context, ns string, ev events.Event) {
	if ev.Kind == EventKindHookFire {
		return
	}
	for _, hook := range d.currentRegistry().MatchTriggers(ns, string(ev.Kind)) {
		_, _ = d.dispatchHook(ctx, hook, ns, ev.Seq)
	}
}

// hookOutcome is one attempt's result before it is folded into a HookFire.
type hookOutcome struct {
	result ResultCode
	err    error
}

// dispatchHook runs one fire and records exactly one HookFire, returning
// it and the scrubbed error. The pipeline after the budget runs in its own
// goroutine; dispatchHook waits for it or for ActionTimeout, whichever is
// first, so a hook that ignores its context cannot hold the loop. An
// abandoned goroutine's late result lands in a buffered channel nobody
// reads and is never audited twice.
func (d *Dispatcher) dispatchHook(parent context.Context, hook HookConfig, ns string, seq uint64) (HookFire, error) {
	fire := Fire{Hook: hook, Namespace: ns, EventSeq: seq}
	fire.Hook.ActionParams = nil
	if !d.Runnable(hook.ActionType) {
		return d.record(hook, fire, hookOutcome{result: ResultRefused, err: newActionNotPermittedError(hook.ActionType)}, nil)
	}
	fire.Chain = chainFor(d.bus, ns, seq)
	if code, err := d.budget.admit(hook.ID, ns, seq, fire.Chain, d.clock.Now()); err != nil {
		return d.record(hook, fire, hookOutcome{result: code, err: err}, nil)
	}
	ctx, cancel := context.WithTimeout(events.WithCause(parent, fire.Chain), d.actionTimeout)
	defer cancel()
	st := &seamState{}
	resultCh := make(chan hookOutcome, 1)
	go func() { resultCh <- d.runAction(ctx, fire, hook.ActionParams, st) }()

	var outcome hookOutcome
	select {
	case outcome = <-resultCh:
	case <-ctx.Done():
		outcome = d.timedOut()
	}
	st.finish()
	return d.record(hook, fire, outcome, st)
}

// timedOut is the outcome of a fire whose ActionTimeout (or parent
// context) ended before the pipeline did.
func (d *Dispatcher) timedOut() hookOutcome {
	return hookOutcome{result: ResultTimeout,
		err: cascade.Newf(cascade.KindTimeout, "hooks: action timed out after %s", d.actionTimeout)}
}

// runAction is the pipeline after the budget: egress pass, routing,
// rehydration seam, runner. A panic anywhere in it is recovered and
// recorded by its type only: its value may quote plaintext. A stage that
// observes the timeout starts nothing later; the runner may start at most
// at the timeout boundary, so a timeout record means outcome unknown.
func (d *Dispatcher) runAction(ctx context.Context, fire Fire, raw map[string]string, st *seamState) (outcome hookOutcome) {
	defer func() {
		if r := recover(); r != nil {
			outcome = hookOutcome{result: ResultPanic, err: cascade.Newf(cascade.KindInternal, "hooks: panic in action (%T)", r)}
		}
	}()
	tagged, err := interceptParams(ctx, d.firewall, d.egressToken, raw)
	if err != nil {
		return hookOutcome{result: ResultRefused, err: err}
	}
	st.setTagged(tagged)
	if refused, ok := d.routeAction(ctx, fire, tagged); !ok {
		return refused
	}
	if ctx.Err() != nil {
		return d.timedOut()
	}
	plain, zero, err := d.rehydrate(ctx, fire, cloneParams(tagged))
	st.setPlain(plain, zero)
	if err != nil {
		return hookOutcome{result: ResultRehydrate, err: seamFailure(err)}
	}
	if ctx.Err() != nil || !st.mayInvoke() {
		return d.timedOut()
	}
	if err := d.invoke(ctx, fire, plain); err != nil {
		return hookOutcome{result: ResultError, err: err}
	}
	return hookOutcome{result: ResultSuccess}
}

// seamFailure is the recorded error for a failed seam: its Kind and a
// fixed text. The seam's own text may quote plaintext it never returned,
// which no scrub pair could catch, so it is never recorded.
func seamFailure(err error) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.New(kind, "hooks: rehydration seam failed")
}

// invoke calls the runner for fire's type: ShellRunner for shell, the
// runner table for everything else.
func (d *Dispatcher) invoke(ctx context.Context, fire Fire, plain map[string]string) error {
	if fire.Hook.ActionType == ActionTypeShell {
		return d.shellRunner.RunShell(ctx, fire.Hook.ID, plain[ShellCommandParam], plain)
	}
	return d.runners[fire.Hook.ActionType].RunAction(ctx, fire, plain)
}
