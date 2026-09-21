// Package topics (reassign_doubles_test.go): Purpose: the two doubles
// AutoThreader.Reassign's tests need - refusingStore (a provider.Store that
// refuses every write, so ExemplarStore.Add can be made to fail past its
// load step) and fakePublisher (a MisfileEventPublisher recording every
// published event). Split from reassign_test.go only to keep both files
// under the 300-line cap (Art.10.3).
package topics

import (
	"context"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// refusingStore is a provider.Store double whose Get always reports
// KindNotFound (so ExemplarStore.load sees "no records yet", not a
// failure) and whose every write/scan method refuses - used to force
// ExemplarStore.Add to fail past its load step, proving Reassign stops
// before publishing when Add fails.
type refusingStore struct{}

var _ provider.Store = refusingStore{}

func (refusingStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "refusingStore: not found")
}

func (refusingStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "refusingStore: put refused")
}

func (refusingStore) Delete(context.Context, string, string) error {
	return cascade.New(cascade.KindUnavailable, "refusingStore: delete refused")
}

func (refusingStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "refusingStore: scan refused")
}

func (refusingStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindUnavailable, "refusingStore: tx refused")
}

// fakePublisher is a MisfileEventPublisher double recording every
// published event, with an injectable error for propagation tests.
type fakePublisher struct {
	err   error
	calls []publishCall
}

type publishCall struct {
	namespace string
	kind      events.EventKind
	source    string
	payload   []byte
}

func (f *fakePublisher) Publish(_ context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	f.calls = append(f.calls, publishCall{namespace: namespace, kind: kind, source: source, payload: payload})
	if f.err != nil {
		return events.Event{}, f.err
	}
	return events.Event{Kind: kind, Source: source, Payload: payload}, nil
}
