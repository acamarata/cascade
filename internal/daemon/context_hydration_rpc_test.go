package daemon

// Purpose: covers RegisterContextHydrationHandler (context_hydration.go),
//   P1-E16-W4-S34-T4's daemon-side registration, through the REAL
//   rpc.Registry.Dispatch entry point over a real events.Bus.
// Constraints: Art.7.1 -- no listener, no real database file; the bus runs
//   over storetest's in-memory store and every clock is injected.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// hydrationRPCFixture wires a registry against a bus whose events are
// stamped at busNow and a handler clock reading handlerNow, so the window
// arithmetic is exercised without any wall clock.
func hydrationRPCFixture(t *testing.T, busNow, handlerNow time.Time) *rpc.Registry {
	t.Helper()
	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(busNow))
	registry := rpc.NewRegistry()
	RegisterContextHydrationHandler(registry, bus, runtime.NewFixedClock(handlerNow))
	return registry
}

// dispatchHydration calls method with params and fails the test on a
// transport-level refusal, returning the handler's result.
func dispatchHydration(t *testing.T, registry *rpc.Registry, method string, params any) any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage(`1`),
	})
	if errObj != nil {
		t.Fatalf("Dispatch(%s) returned error: %+v", method, errObj)
	}
	return result
}

// TestContextHydrationDegradedIsRecordedAndThenCounted is the round trip
// the doctor check depends on: the hook's publish and the check's count
// are two different methods over one log, and a test that only exercised
// the write would pass while the read answered zero forever.
func TestContextHydrationDegradedIsRecordedAndThenCounted(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	registry := hydrationRPCFixture(t, now, now)

	published := dispatchHydration(t, registry, ContextHydrationDegradedMethod,
		ContextHydrationDegradedParams{Reason: "the retrieval index was empty"})
	got, ok := published.(ContextHydrationDegradedResult)
	if !ok {
		t.Fatalf("degraded result type = %T, want ContextHydrationDegradedResult", published)
	}
	if got.Seq == 0 {
		t.Error("Seq = 0: a caller cannot tell a recorded degradation from a dropped one")
	}

	reported := dispatchHydration(t, registry, ContextHydrationReportMethod, ContextHydrationReportParams{})
	report, ok := reported.(ContextHydrationReportResult)
	if !ok {
		t.Fatalf("report result type = %T, want ContextHydrationReportResult", reported)
	}
	if report.Count != 1 {
		t.Errorf("Count = %d after one published degradation, want 1", report.Count)
	}
	if report.WindowSeconds != int(hydration.DegradedWindow/time.Second) {
		t.Errorf("WindowSeconds = %d, want the ruled default %d",
			report.WindowSeconds, int(hydration.DegradedWindow/time.Second))
	}
}

// TestContextHydrationReportHonoursItsWindow pins the half of the report
// that decides whether an operator is shown an old problem as a current
// one: the same log answers 0 for the default window and 1 for a window
// wide enough to reach the event.
func TestContextHydrationReportHonoursItsWindow(t *testing.T) {
	busNow := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	registry := hydrationRPCFixture(t, busNow, busNow.Add(48*time.Hour))

	dispatchHydration(t, registry, ContextHydrationDegradedMethod,
		ContextHydrationDegradedParams{Reason: "two days ago"})

	stale := dispatchHydration(t, registry, ContextHydrationReportMethod, ContextHydrationReportParams{})
	if count := stale.(ContextHydrationReportResult).Count; count != 0 {
		t.Errorf("Count = %d for an event 48h old under a 24h window, want 0", count)
	}

	wide := dispatchHydration(t, registry, ContextHydrationReportMethod,
		ContextHydrationReportParams{WindowSeconds: int((7 * 24 * time.Hour) / time.Second)})
	report := wide.(ContextHydrationReportResult)
	if report.Count != 1 {
		t.Errorf("Count = %d for the same event under a 7-day window, want 1", report.Count)
	}
	if report.WindowSeconds != int((7*24*time.Hour)/time.Second) {
		t.Errorf("WindowSeconds = %d, want the caller's own window echoed back", report.WindowSeconds)
	}
}

// TestContextHydrationDegradedRefusesAnEmptyReason: an event carrying no
// reason is worse than no event, because the doctor check counts it and
// then has nothing to tell the operator.
func TestContextHydrationDegradedRefusesAnEmptyReason(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	registry := hydrationRPCFixture(t, now, now)

	for _, tc := range []struct {
		name   string
		params json.RawMessage
	}{
		{"empty reason", json.RawMessage(`{"reason":""}`)},
		{"no params at all", json.RawMessage(`{}`)},
		{"params that are not an object", json.RawMessage(`"degraded"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
				JSONRPC: "2.0", Method: ContextHydrationDegradedMethod,
				Params: tc.params, ID: json.RawMessage(`1`),
			})
			if errObj == nil {
				t.Fatal("Dispatch accepted a degradation with no reason")
			}
			if errObj.Code != cascade.KindInvalidInput.JSONRPCCode() {
				t.Errorf("error code = %d, want the KindInvalidInput code %d",
					errObj.Code, cascade.KindInvalidInput.JSONRPCCode())
			}
		})
	}
}

// TestContextHydrationReportRefusesUndecodableParams keeps the report's
// decode path from silently falling back to the default window on
// garbage, which would report a number nobody asked for.
func TestContextHydrationReportRefusesUndecodableParams(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	registry := hydrationRPCFixture(t, now, now)

	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextHydrationReportMethod,
		Params: json.RawMessage(`{"window_seconds":"a week"}`), ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch accepted a report request whose window could not be decoded")
	}
}

// TestRegisterContextHydrationHandlerWithoutABusRegistersNothing pins the
// documented nil behaviour, which is load-bearing: the hook's fallback is
// to publish directly, and a registered method that always refused would
// make it stop trying. The positive case above is what proves this
// assertion can fail.
func TestRegisterContextHydrationHandlerWithoutABusRegistersNothing(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		bus   *events.Bus
		clock runtime.Clock
	}{
		{"nil bus", nil, runtime.NewFixedClock(now)},
		{"nil clock", events.New(storetest.NewMemStore(), runtime.NewFixedClock(now)), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := rpc.NewRegistry()
			RegisterContextHydrationHandler(registry, tc.bus, tc.clock)
			for _, method := range []string{ContextHydrationDegradedMethod, ContextHydrationReportMethod} {
				if registry.Registered(method) {
					t.Errorf("%s was registered against a half-wired daemon", method)
				}
			}
		})
	}
}
