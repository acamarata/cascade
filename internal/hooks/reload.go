package hooks

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: hot reload of the hook set. A reload event re-reads the config
//
//	section, parses it, builds a fresh Registry and swaps it into the
//	dispatcher; any failure keeps the previous set and publishes a
//	rejection that names the error and never a param.
//
// Inputs: the bus namespace and event kind reloads are announced on, a
//
//	section source, a parser (ParseHooksSection bound to the dispatcher's
//	Runnable), and the Dispatcher.
//
// Outputs: Dispatcher.Swap on success; one EventKindReloadRejected event
//
//	on the reload namespace per refused reload.
//
// Constraints: WatchReload subscribes before the composition's initial
//
//	build, so a reload accepted between that build and Run is still
//	delivered (at worst one redundant re-read).

// EventKindReloadRejected is published on the reload namespace when a
// reload leaves the previous hook set in force.
const EventKindReloadRejected events.EventKind = "hooks.reload.rejected"

// reloadBuffer is the reload subscription's channel capacity.
const reloadBuffer = 8

// ReloadWatch is a live subscription to reload announcements. Construct
// with WatchReload; Run consumes it.
type ReloadWatch struct {
	bus  *events.Bus
	ns   string
	kind events.EventKind
	sub  *events.Subscription
}

// WatchReload subscribes cursor to ns from its head, synchronously, and
// returns the watch Run consumes.
func WatchReload(ctx context.Context, bus *events.Bus, ns string, kind events.EventKind, cursor string) (*ReloadWatch, error) {
	if bus == nil || ns == "" || kind == "" || cursor == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "hooks: reload: bus, namespace, kind and cursor are required")
	}
	sub, err := bus.SubscribeFromHead(ctx, ns, cursor, reloadBuffer)
	if err != nil {
		return nil, err
	}
	return &ReloadWatch{bus: bus, ns: ns, kind: kind, sub: sub}, nil
}

// Run applies every reload event until ctx is canceled or the
// subscription fails, then releases the subscription.
func (w *ReloadWatch) Run(ctx context.Context, src func() (any, error), parse func(any) ([]HookConfig, error), d *Dispatcher) error {
	defer func() { _ = w.sub.Unsubscribe() }()
	if src == nil || parse == nil || d == nil {
		return cascade.New(cascade.KindInvalidInput, "hooks: reload: source, parser and dispatcher are required")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.sub.Events:
			if !ok {
				select {
				case err := <-w.sub.Errs:
					return err
				default:
					return nil
				}
			}
			if ev.Kind == w.kind {
				w.apply(ctx, src, parse, d)
			}
		}
	}
}

// apply performs one reload, publishing a rejection on any failure.
func (w *ReloadWatch) apply(ctx context.Context, src func() (any, error), parse func(any) ([]HookConfig, error), d *Dispatcher) {
	raw, err := src()
	if err != nil {
		// The source's own text may quote the file it failed on, values
		// included, so only its kind is published.
		kind, _ := cascade.KindOf(err)
		w.reject(ctx, "hooks: reload source unreadable ("+kind.String()+")")
		return
	}
	cfgs, err := parse(raw)
	if err != nil {
		w.reject(ctx, err.Error())
		return
	}
	reg := NewRegistry()
	for _, cfg := range cfgs {
		if _, err := reg.Register(cfg); err != nil {
			w.reject(ctx, err.Error())
			return
		}
	}
	if err := d.Swap(reg); err != nil {
		w.reject(ctx, err.Error())
	}
}

// reject publishes the rejection. Publish errors are dropped: the previous
// set stays in force either way.
func (w *ReloadWatch) reject(ctx context.Context, msg string) {
	payload, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		return
	}
	_, _ = w.bus.Publish(ctx, w.ns, EventKindReloadRejected, SectionName, payload)
}
