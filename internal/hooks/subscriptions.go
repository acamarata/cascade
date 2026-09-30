package hooks

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: one bus subscription per namespace the registry names, started
//
//	by Run and changed atomically by Swap, with the configured namespace
//	set persisted so a namespace the dispatcher stopped following never
//	replays its backlog when it comes back.
//
// Inputs: the registry's Namespaces, the bus, and the State store.
//
// Outputs: a consumer goroutine per namespace calling handleEvent.
//
// Constraints: a namespace starts at its head whenever the dispatcher
//
//	begins following it (SubscribeFromHead, plus a reset for a cursor left
//	behind by an earlier configuration). The persisted set never names a
//	namespace nobody follows: a running Swap resets and subscribes every
//	added namespace without persisting it, then writes the new set once as
//	its commit point while removed namespaces are still followed. A failure
//	up to and including that write stops the added subscriptions and keeps
//	the previous set and registry. A removed namespace's cursor is reset
//	after the commit, best effort: it is outside the set, so the next add
//	or Run resets it. A crash at any point leaves every stale cursor either
//	outside the set or unconfigured, and Run's start resets both.

// dispatcherStateNamespace and namespacesKey locate the persisted set.
const (
	dispatcherStateNamespace = "hooks.dispatcher"
	namespacesKey            = "namespaces"
)

// nsSub is one namespace's live subscription and consumer goroutine.
type nsSub struct {
	sub    *events.Subscription
	stop   chan struct{}
	exited chan struct{}
}

// Run follows every namespace the registry names until ctx is canceled or a
// subscription fails. At start, every namespace whose membership differs
// between the persisted set and the configured set has its cursor reset to
// head. Run may be called again after it returns.
func (d *Dispatcher) Run(ctx context.Context) error {
	errs, err := d.start(ctx)
	if err != nil {
		return err
	}
	return d.serve(ctx, errs)
}

// serve waits for the end of a started Run and stops every consumer.
func (d *Dispatcher) serve(ctx context.Context, errs <-chan error) error {
	defer d.stopAll()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

// start reconciles cursors and subscribes every configured namespace.
func (d *Dispatcher) start(ctx context.Context) (<-chan error, error) {
	d.swapMu.Lock()
	defer d.swapMu.Unlock()
	if d.running {
		return nil, cascade.New(cascade.KindConflict, "hooks: dispatcher is already running")
	}
	want := d.currentRegistry().Namespaces()
	if err := d.reconcileAtStart(ctx, want); err != nil {
		return nil, err
	}
	d.runCtx, d.errs, d.subs = ctx, make(chan error, 1), make(map[string]*nsSub)
	for _, ns := range want {
		if err := d.subscribeLocked(ns); err != nil {
			d.stopSubsLocked()
			return nil, err
		}
	}
	d.running = true
	return d.errs, nil
}

// reconcileAtStart resets to head every namespace in exactly one of the
// persisted and configured sets, then persists the configured set.
func (d *Dispatcher) reconcileAtStart(ctx context.Context, want []string) error {
	persisted, err := d.loadNamespaceSet(ctx)
	if err != nil {
		return err
	}
	for _, ns := range symmetricDifference(persisted, want) {
		if err := d.bus.ResetCursorToHead(ctx, ns, d.cursorPrefix+ns); err != nil {
			return err
		}
	}
	return d.saveNamespaceSet(ctx, want)
}

// Swap replaces the registry atomically. Namespaces the new registry adds
// start at their head and namespaces it drops are unsubscribed (while
// running) and reset to head, so a namespace removed and later re-added
// never replays what was published while it was gone. A registry holding
// an unrunnable hook, or a failure before the commit, changes nothing.
func (d *Dispatcher) Swap(reg *Registry) error {
	if reg == nil {
		return cascade.New(cascade.KindInvalidInput, "hooks: swap: registry is required")
	}
	for _, h := range reg.List() {
		if !d.Runnable(h.ActionType) {
			return newActionNotPermittedError(h.ActionType)
		}
	}
	d.swapMu.Lock()
	defer d.swapMu.Unlock()
	if !d.running {
		// Stopped: the next Run follows reg, so every namespace reg adds
		// or drops relative to the persisted set starts at head now.
		if err := d.reconcileAtStart(context.Background(), reg.Namespaces()); err != nil {
			return err
		}
		d.setRegistry(reg)
		return nil
	}
	return d.resubscribeLocked(reg)
}

// resubscribeLocked applies reg's namespaces to the live dispatcher. The
// commit point is the namespace-set write in commitAddedLocked; reg is
// installed only after it. Caller holds swapMu. After the commit, removed
// namespaces are unsubscribed and their cursors reset best effort: they
// are outside the persisted set, so the next add or Run resets them.
func (d *Dispatcher) resubscribeLocked(reg *Registry) error {
	want, live := reg.Namespaces(), make([]string, 0, len(d.subs))
	for ns := range d.subs {
		live = append(live, ns)
	}
	var added, removed []string
	for _, ns := range symmetricDifference(want, live) {
		if contains(want, ns) {
			added = append(added, ns)
		} else {
			removed = append(removed, ns)
		}
	}
	if err := d.commitAddedLocked(want, added); err != nil {
		return err
	}
	d.setRegistry(reg)
	for _, ns := range removed {
		d.stopSub(d.subs[ns])
		delete(d.subs, ns)
		_ = d.bus.ResetCursorToHead(d.runCtx, ns, d.cursorPrefix+ns)
	}
	return nil
}

// commitAddedLocked resets and subscribes each added namespace without
// persisting it, then persists want: the single commit point. On any
// failure it stops the added subscriptions and leaves the persisted set
// as it was, which is the live set.
func (d *Dispatcher) commitAddedLocked(want, added []string) error {
	var err error
	for i := 0; err == nil && i < len(added); i++ {
		ns := added[i]
		if err = d.bus.ResetCursorToHead(d.runCtx, ns, d.cursorPrefix+ns); err == nil {
			err = d.subscribeLocked(ns)
		}
	}
	if err == nil {
		err = d.saveNamespaceSet(d.runCtx, want)
	}
	if err != nil {
		for _, ns := range added {
			if s, ok := d.subs[ns]; ok {
				d.stopSub(s)
				delete(d.subs, ns)
			}
		}
	}
	return err
}

// setRegistry installs reg.
func (d *Dispatcher) setRegistry(reg *Registry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.registry = reg
}

// subscribeLocked starts following ns. Caller holds swapMu.
func (d *Dispatcher) subscribeLocked(ns string) error {
	sub, err := d.bus.SubscribeFromHead(d.runCtx, ns, d.cursorPrefix+ns, d.subscribeBuffer)
	if err != nil {
		return err
	}
	s := &nsSub{sub: sub, stop: make(chan struct{}), exited: make(chan struct{})}
	d.subs[ns] = s
	go d.consume(d.runCtx, ns, s)
	return nil
}

// consume dispatches ns's events until stopped or the subscription ends.
// A subscription that ends with an error hands it to Run.
func (d *Dispatcher) consume(ctx context.Context, ns string, s *nsSub) {
	defer close(s.exited)
	for {
		select {
		case <-s.stop:
			return
		case ev, ok := <-s.sub.Events:
			if !ok {
				d.forwardSubError(s)
				return
			}
			select {
			case <-s.stop:
				return
			default:
			}
			d.handleEvent(ctx, ns, ev)
		}
	}
}

// forwardSubError passes a fatal subscription error to Run, if there is one.
func (d *Dispatcher) forwardSubError(s *nsSub) {
	select {
	case err := <-s.sub.Errs:
		select {
		case d.errs <- err:
		default:
		}
	default:
	}
}

// stopSub stops one consumer and waits for it to exit.
func (d *Dispatcher) stopSub(s *nsSub) {
	close(s.stop)
	_ = s.sub.Unsubscribe()
	<-s.exited
}

// stopSubsLocked stops every consumer. Caller holds swapMu.
func (d *Dispatcher) stopSubsLocked() {
	for ns, s := range d.subs {
		d.stopSub(s)
		delete(d.subs, ns)
	}
}

// stopAll ends a Run.
func (d *Dispatcher) stopAll() {
	d.swapMu.Lock()
	defer d.swapMu.Unlock()
	d.stopSubsLocked()
	d.running = false
}

// loadNamespaceSet reads the persisted set; absent reads as empty.
func (d *Dispatcher) loadNamespaceSet(ctx context.Context) ([]string, error) {
	raw, err := d.state.Get(ctx, dispatcherStateNamespace, namespacesKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "hooks: reading the persisted namespace set")
	}
	var set []string
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "hooks: the persisted namespace set is unreadable")
	}
	return set, nil
}

// saveNamespaceSet persists set.
func (d *Dispatcher) saveNamespaceSet(ctx context.Context, set []string) error {
	if set == nil {
		set = []string{}
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "hooks: encoding the namespace set")
	}
	if err := d.state.Put(ctx, dispatcherStateNamespace, namespacesKey, raw); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "hooks: persisting the namespace set")
	}
	return nil
}
