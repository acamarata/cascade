package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// statsRegistry returns a registry with notify.stats over a Service that
// has dropped two queue-full items carrying a secret body.
func statsRegistry(t *testing.T) (*rpc.Registry, *Service) {
	t.Helper()
	svc, _ := newAddressedService(t)
	for _, id := range []string{"d1", "d2"} {
		svc.Dispatcher.queues.drop(Notification{ID: id, Source: "src", Payload: []byte("secret-body")}, PriorityLow, DropQueueFull)
	}
	reg := rpc.NewRegistry()
	if err := RegisterStatsRPC(reg, svc); err != nil {
		t.Fatalf("RegisterStatsRPC: %v", err)
	}
	return reg, svc
}

// TestNotifyStatsOwnerOnly: a principal caller (and a caller-less request)
// is refused KindPermissionDenied; the owner caller gets the per-reason
// counters and no notification body.
func TestNotifyStatsOwnerOnly(t *testing.T) {
	reg, svc := statsRegistry(t)
	for name, ctx := range map[string]context.Context{
		"principal": asPrincipal("p1", "s1"),
		"no caller": context.Background(),
	} {
		if _, eo := reg.Dispatch(ctx, &rpc.Request{Method: StatsMethod}); eo == nil {
			t.Fatalf("%s caller got notify.stats; want refusal", name)
		}
	}
	if _, err := callStats(asPrincipal("p1", "s1"), svc); err == nil {
		t.Fatal("principal handler call succeeded")
	} else if k, _ := cascade.KindOf(err); k != cascade.KindPermissionDenied {
		t.Fatalf("principal refusal kind = %v, want KindPermissionDenied", k)
	}
	res, eo := reg.Dispatch(asOwnerCLI(), &rpc.Request{Method: StatsMethod})
	if eo != nil {
		t.Fatalf("owner notify.stats: %+v", eo)
	}
	st, ok := res.(Stats)
	if !ok {
		t.Fatalf("result type %T, want Stats", res)
	}
	if st.Dropped[DropQueueFull] != 2 || len(st.Dropped) != 1 {
		t.Fatalf("Dropped = %v, want {%s: 2}", st.Dropped, DropQueueFull)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "secret-body") || !strings.Contains(string(raw), `"queue-full":2`) {
		t.Fatalf("result %s leaks a body or lacks the counter", raw)
	}
}

// callStats invokes the registered handler directly, bypassing any
// registry access policy, so the handler's own owner check is exercised.
func callStats(ctx context.Context, svc *Service) (any, error) {
	reg := rpc.NewRegistry()
	if err := RegisterStatsRPC(reg, svc); err != nil {
		return nil, err
	}
	res, eo := reg.Dispatch(ctx, &rpc.Request{Method: StatsMethod})
	if eo != nil {
		return nil, cascade.New(cascade.KindPermissionDenied, eo.Message)
	}
	return res, nil
}

// TestNotifyStatsDeclaredOwnerOnly: notify.stats is refused to a principal by
// the registry's access policy too, not only by the handler.
func TestNotifyStatsDeclaredOwnerOnly(t *testing.T) {
	reg, _ := statsRegistry(t)
	reg.SetAccessPolicy(nil)
	if _, eo := reg.Dispatch(asPrincipal("p1", "s1"), &rpc.Request{Method: StatsMethod}); eo == nil {
		t.Fatal("access policy let a principal call notify.stats")
	}
	if _, eo := reg.Dispatch(asOwnerCLI(), &rpc.Request{Method: StatsMethod}); eo != nil {
		t.Fatalf("access policy refused the owner: %+v", eo)
	}
}

// TestSetDropSinkOnce: the first sink receives each drop; a second
// SetDropSink returns KindConflict and leaves the first in place.
func TestSetDropSinkOnce(t *testing.T) {
	svc, _ := newAddressedService(t)
	var got []DropEvent
	if err := svc.SetDropSink(func(_ context.Context, e DropEvent) { got = append(got, e) }); err != nil {
		t.Fatalf("SetDropSink: %v", err)
	}
	err := svc.SetDropSink(func(context.Context, DropEvent) { t.Error("second sink called") })
	if k, _ := cascade.KindOf(err); k != cascade.KindConflict {
		t.Fatalf("second SetDropSink kind = %v (%v), want KindConflict", k, err)
	}
	svc.Dispatcher.queues.drop(Notification{ID: "d1", Source: "src"}, PriorityHigh, DropQueueFull)
	if len(got) != 1 || got[0].ID != "d1" || got[0].Reason != DropQueueFull {
		t.Fatalf("sink got %+v, want one d1 queue-full event", got)
	}
	if st := svc.Stats(); st.Dropped[DropQueueFull] != 1 || st.Accumulated != 0 {
		t.Fatalf("Stats = %+v", st)
	}
}
