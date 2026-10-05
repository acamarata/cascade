// Purpose: the pure classifier's table test, and the observation-slot and
//
//	Transport-wrapper unit tests (what the slot keeps, what it refuses to).
//
// Inputs: constructed errors, statuses and header maps.
// Outputs: none.
// Constraints: no driver, no network; time is a fixed instant.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClassifyOutcome(t *testing.T) {
	now := laneHarnessNow
	retry := func(v string) map[string][]string { return map[string][]string{"Retry-After": {v}} }
	kinded := func(k cascade.Kind) error { return cascade.New(k, "test") }
	cases := []struct {
		name    string
		err     error
		status  int
		headers map[string][]string
		state   registry.LaneState
		reset   time.Time
		write   bool
	}{
		{"success", nil, 200, nil, registry.LaneStateAvailable, time.Time{}, true},
		{"success ignores retry-after", nil, 200, retry("30"), registry.LaneStateAvailable, time.Time{}, true},
		{"success without a response", nil, 0, nil, "", time.Time{}, false},
		{"permission denied", kinded(cascade.KindPermissionDenied), 401, nil, registry.LaneStateAuthRequired, time.Time{}, true},
		{"permission denied without a response", kinded(cascade.KindPermissionDenied), 0, nil, "", time.Time{}, false},
		{"permission denied on a 400 the driver typed so", kinded(cascade.KindPermissionDenied), 400, nil, registry.LaneStateAuthRequired, time.Time{}, true},
		{"capability denied on 403", kinded(cascade.KindCapabilityDenied), 403, nil, "", time.Time{}, false},
		{"quota with retry-after", kinded(cascade.KindQuotaExhausted), 429, retry("120"), registry.LaneStateExhausted, now.Add(120 * time.Second), true},
		{"quota with zero retry-after", kinded(cascade.KindQuotaExhausted), 429, retry("0"), registry.LaneStateExhausted, now, true},
		{"quota with padded value", kinded(cascade.KindQuotaExhausted), 429, retry(" 60 "), registry.LaneStateExhausted, now.Add(time.Minute), true},
		{"quota with plus sign", kinded(cascade.KindQuotaExhausted), 429, retry("+60"), registry.LaneStateExhausted, time.Time{}, true},
		{"quota with fraction", kinded(cascade.KindQuotaExhausted), 429, retry("1.5"), registry.LaneStateExhausted, time.Time{}, true},
		{"quota with empty value", kinded(cascade.KindQuotaExhausted), 429, retry(""), registry.LaneStateExhausted, time.Time{}, true},
		{"quota with an empty header entry", kinded(cascade.KindQuotaExhausted), 429, map[string][]string{"Retry-After": {}}, registry.LaneStateExhausted, time.Time{}, true},
		{"quota without a response", kinded(cascade.KindQuotaExhausted), 0, retry("5"), "", time.Time{}, false},
		{"unavailable", kinded(cascade.KindUnavailable), 503, nil, "", time.Time{}, false},
		{"internal", kinded(cascade.KindInternal), 500, nil, "", time.Time{}, false},
		{"invalid input", kinded(cascade.KindInvalidInput), 400, nil, "", time.Time{}, false},
		{"canceled", kinded(cascade.KindCanceled), 200, nil, "", time.Time{}, false},
		{"untyped error", errors.New("plain"), 401, nil, "", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, reset, write := classifyOutcome(tc.err, tc.status, tc.headers, now)
			if state != tc.state || !reset.Equal(tc.reset) || write != tc.write {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", state, reset, write, tc.state, tc.reset, tc.write)
			}
		})
	}
}

// sendOnce drives one observingTransport.Send over a scripted response.
func sendOnce(ctx context.Context, tr *scriptTransport) error {
	_, _, body, err := observingTransport{inner: tr}.Send(ctx, "POST", "https://example.invalid/x",
		map[string]string{"Authorization": "Bearer " + canaryCredential()}, []byte("request-body"))
	if body != nil {
		_, _ = io.Copy(io.Discard, body)
	}
	return err
}

func TestObservationKeepsOnlyStatusAndRetryAfter(t *testing.T) {
	ctx, slot := withObservation(context.Background())
	tr := &scriptTransport{responses: []scriptedResponse{{status: 429, headers: map[string][]string{
		"retry-after": {"7"}, "Set-Cookie": {"session=secret"}, "X-Request-Id": {"abc"}}}}}
	if err := sendOnce(ctx, tr); err != nil {
		t.Fatalf("send: %v", err)
	}
	status, headers := slot.snapshot()
	if status != 429 || len(headers) != 1 || len(headers["Retry-After"]) != 1 || headers["Retry-After"][0] != "7" {
		t.Fatalf("snapshot = %d %v, want 429 and only Retry-After: 7", status, headers)
	}
}

func TestObservationKeepsTheLastResponseAndIgnoresErrors(t *testing.T) {
	ctx, slot := withObservation(context.Background())
	tr := &scriptTransport{responses: []scriptedResponse{
		{status: 429, headers: map[string][]string{"Retry-After": {"9"}}},
		{status: 200},
		{err: errAlwaysFails},
	}}
	for i := 0; i < 3; i++ {
		_ = sendOnce(ctx, tr)
	}
	if status, headers := slot.snapshot(); status != 200 || headers != nil {
		t.Fatalf("snapshot = %d %v, want the last response (200, no headers); a failed send must not clear or set it", status, headers)
	}
	if status, _ := (&observation{}).snapshot(); status != 0 {
		t.Fatalf("an empty slot reports status %d, want 0", status)
	}
}

func TestObservationSlotsAreNotSharedAcrossCalls(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		status := 200 + i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, slot := withObservation(context.Background())
			if err := sendOnce(ctx, &scriptTransport{responses: []scriptedResponse{{status: status}}}); err != nil {
				t.Errorf("send: %v", err)
			}
			if got, _ := slot.snapshot(); got != status {
				t.Errorf("slot saw status %d, want its own call's %d", got, status)
			}
		}()
	}
	wg.Wait()
}

func TestObservingTransportWithoutASlotPassesThrough(t *testing.T) {
	tr := &scriptTransport{responses: []scriptedResponse{{status: 200, body: "ok"}}}
	if err := sendOnce(context.Background(), tr); err != nil || tr.calls != 1 {
		t.Fatalf("send without a slot: err %v, %d calls, want a plain pass-through", err, tr.calls)
	}
}
