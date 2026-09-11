// Purpose: table-driven tests for NormalizeEvent (stateless variant
//
//	mapping) and EventNormalizer (stateful ordering/dedup enforcement).
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNormalizeEventKnownVariants(t *testing.T) {
	cases := []struct {
		name string
		kind provider.DriverEventKind
	}{
		{"stdout", provider.DriverEventStdout},
		{"status", provider.DriverEventStatus},
		{"error", provider.DriverEventError},
		{"done", provider.DriverEventDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := provider.NormalizeEvent(provider.DriverEvent{Kind: c.kind, Seq: 1, DataClass: provider.DataClassPublic})
			if err != nil {
				t.Fatalf("NormalizeEvent(%s): %v", c.name, err)
			}
			if ev.Kind != c.kind {
				t.Fatalf("NormalizeEvent(%s).Kind = %v, want %v", c.name, ev.Kind, c.kind)
			}
			if ev.DataClass != provider.DataClassPublic {
				t.Fatalf("NormalizeEvent(%s).DataClass = %v, want public", c.name, ev.DataClass)
			}
		})
	}
}

func TestNormalizeEventUnknownVariant(t *testing.T) {
	ev, err := provider.NormalizeEvent(provider.DriverEvent{Kind: provider.DriverEventUnknown})
	if err == nil {
		t.Fatal("NormalizeEvent(unknown) returned nil error, want a typed error")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NormalizeEvent(unknown) err = %v, want KindInvalidInput", err)
	}
	if ev != (provider.AgentEvent{}) {
		t.Fatalf("NormalizeEvent(unknown) ev = %+v, want zero value", ev)
	}
	// An out-of-range Kind (never assigned by any constant) must also
	// refuse rather than fall through to a permissive default.
	if _, err := provider.NormalizeEvent(provider.DriverEvent{Kind: provider.DriverEventKind(99)}); err == nil {
		t.Fatal("NormalizeEvent(out-of-range kind) returned nil error")
	}
}

func TestEventOrderingAndDedup(t *testing.T) {
	n := provider.NewEventNormalizer()

	ev, keep, err := n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 1, DedupKey: "a"})
	if err != nil || !keep {
		t.Fatalf("first event: (%v, %v, %v), want (_, true, nil)", ev, keep, err)
	}

	_, keep, err = n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 2, DedupKey: "a"})
	if err != nil || keep {
		t.Fatalf("repeated DedupKey: (keep=%v, err=%v), want (false, nil): dropped, not an error", keep, err)
	}

	_, keep, err = n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 1, DedupKey: "b"})
	if err != provider.ErrOutOfOrderEvent || keep {
		t.Fatalf("Seq regression: (keep=%v, err=%v), want (false, ErrOutOfOrderEvent)", keep, err)
	}

	_, keep, err = n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 3, DedupKey: "c"})
	if err != nil || !keep {
		t.Fatalf("advancing Seq: (keep=%v, err=%v), want (true, nil)", keep, err)
	}
}
