package plugins

// Purpose (this file): unit coverage for cascadepa_install_events.go's
//   busEventPublisher adapter, driven by an in-memory installEventPublisher
//   double (never a real provider.Store) -- the namespace/kind/source
//   mapping and the nil-bus / marshal-failure / bus-error propagation
//   paths.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// recordingEventPublisher is an in-memory installEventPublisher double.
type recordingEventPublisher struct {
	namespace string
	kind      events.EventKind
	source    string
	payload   []byte
	err       error
	calls     int
}

func (r *recordingEventPublisher) Publish(_ context.Context, namespace string, kind events.EventKind,
	source string, payload []byte) (events.Event, error) {
	r.calls++
	r.namespace, r.kind, r.source, r.payload = namespace, kind, source, payload
	if r.err != nil {
		return events.Event{}, r.err
	}
	return events.Event{}, nil
}

func TestBusEventPublisher_NilBusRefuses(t *testing.T) {
	p := newBusEventPublisher(nil)
	err := p.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal})
	if err == nil {
		t.Fatal("Publish: err = nil, want a refusal for an unwired bus")
	}
}

func TestBusEventPublisher_PublishesUnderInstallNamespace(t *testing.T) {
	rec := &recordingEventPublisher{}
	p := newBusEventPublisher(rec)
	evt := install.Event{Kind: install.EventInstallProposal,
		Proposal: &install.Proposal{Intent: "github:push", PluginID: "gh"}}
	if err := p.Publish(context.Background(), evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("bus.Publish called %d times, want 1", rec.calls)
	}
	if rec.namespace != installEventNamespace {
		t.Errorf("namespace = %q, want %q", rec.namespace, installEventNamespace)
	}
	if rec.source != installEventSource {
		t.Errorf("source = %q, want %q", rec.source, installEventSource)
	}
	if string(rec.kind) != string(install.EventInstallProposal) {
		t.Errorf("kind = %q, want %q", rec.kind, install.EventInstallProposal)
	}
	if len(rec.payload) == 0 {
		t.Error("payload is empty")
	}
}

func TestBusEventPublisher_BusErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: store unavailable")
	rec := &recordingEventPublisher{err: wantErr}
	p := newBusEventPublisher(rec)
	err := p.Publish(context.Background(), install.Event{Kind: install.EventInstallDeclined,
		Declined: &install.Declined{PluginID: "gh", Reason: install.DeclineExplicit}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Publish: err = %v, want it to wrap %v", err, wantErr)
	}
}
