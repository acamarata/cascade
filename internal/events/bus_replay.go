// Purpose: Bus.Replay (task 5) and replayFrom, the shared historical-scan
//
//	helper both Replay and deliverLoop's pull (bus_subscribe.go) use — one
//	scan implementation, one place that owns "what does 'forward from
//	offset' mean" (bus.go's package doc: EXCLUSIVE of offset).
//
// Constraints: Replay is a bounded, point-in-time read of whatever the
//
//	Store holds AT CALL TIME — it does not block waiting for future
//	events (that live-tail behavior belongs to Subscribe). Split from
//	bus.go/bus_subscribe.go under R-14.117's authorized-split allowance to
//	keep both files under Art.10.3's 300-line cap.
//
// SPORT: internal.events.Bus/ADDED (Replay) (P1-E03-W1-S04-T3).

package events

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Replay returns every event in namespace with Seq > offset, in strictly
// increasing Seq order, as of the moment Replay is called. It never drops
// an event in that range (acceptance criterion) and never blocks for
// events published after the call starts — callers that also want live
// events use Subscribe, which internally replays from a cursor and then
// continues tailing.
func (b *Bus) Replay(ctx context.Context, namespace string, offset uint64) ([]Event, error) {
	return replayFrom(ctx, b.store, namespace, offset)
}

// replayFrom scans namespace's persisted event records and returns those
// with Seq > offset, sorted by Seq. Store.Scan already documents "in key
// order," and eventKey's zero-padding makes key order equal Seq order, but
// this function sorts explicitly anyway rather than depending on a driver
// obeying that ordering exactly — a cheap, defensive belt-and-suspenders
// given how central delivery order is to every acceptance criterion here.
func replayFrom(ctx context.Context, store provider.Store, namespace string, offset uint64) ([]Event, error) {
	it, err := store.Scan(ctx, namespace, eventKeyPrefix)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "events: replay scan namespace %q", namespace)
	}
	defer func() { _ = it.Close() }()

	var out []Event
	for it.Next(ctx) {
		ev, decErr := decodeEvent(it.Value())
		if decErr != nil {
			return nil, decErr
		}
		if ev.Seq <= offset {
			continue
		}
		out = append(out, ev.clone())
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, iterErr, "events: replay iterating namespace %q", namespace)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
