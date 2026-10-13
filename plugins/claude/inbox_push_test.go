package claude

// Purpose: proves InboxPush's routing (P1-E37-W8-S73-T1): addressed events
// push directly when the channel negotiated and are then NOT hydrated;
// deferred, rejected and Windows channels fall back to durable hydration;
// mismatched addressed targets are withheld; cancellation, not-started and
// shutdown races refuse with typed errors and deliver nothing.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fixedNow is the injected clock every routing test reads.
var fixedNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// newRouter builds a started InboxPush over a memory store and ch.
func newRouter(t *testing.T, ch *CCChannel) (*InboxPush, *InboxHydration) {
	t.Helper()
	h, err := NewInboxHydration(storetest.NewMemStore(), func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewInboxPush(ch, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p, h
}

// pending drains sid and returns the record ids.
func pending(t *testing.T, h *InboxHydration, sid string) []string {
	t.Helper()
	recs, _, err := h.AppendNextTurn(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids
}

func addressedTo1(id string) InboxEvent {
	return InboxEvent{ID: id, Class: "addressed", TargetSession: "s1", Visibility: "private", Body: []byte("hello there")}
}

func TestInboxPushDirectDeliveryIsNotAlsoHydrated(t *testing.T) {
	calls, log := 0, &frameLog{}
	p, h := newRouter(t, newCCChannel("linux", supportedProbe(fixtureHandshake(t), false, &calls), log.send))
	res, err := p.Route(context.Background(), "s1", addressedTo1("d1"))
	if err != nil || !res.Appended || res.SessionID != "s1" || log.count() != 1 {
		t.Fatalf("direct push = (%+v, %v), frames %d", res, err, log.count())
	}
	if ids := pending(t, h, "s1"); len(ids) != 0 {
		t.Fatalf("a directly pushed event was also hydrated: %v", ids)
	}
	if res, _ := p.Route(context.Background(), "s1", addressedTo1("d1")); !res.Appended || log.count() != 1 {
		t.Fatalf("a redelivered id sent again: frames %d", log.count())
	}
}

func TestInboxPushFallsBackToHydration(t *testing.T) {
	calls := 0
	rejecting := &frameLog{fail: true}
	for name, ch := range map[string]*CCChannel{
		"deferred": NewCCChannel(),
		"rejected": newCCChannel("linux", supportedProbe(fixtureHandshake(t), false, &calls), rejecting.send),
		"windows":  newCCChannel("windows", supportedProbe(fixtureHandshake(t), false, &calls), rejecting.send),
	} {
		p, h := newRouter(t, ch)
		res, err := p.Route(context.Background(), "s1", addressedTo1("f-"+name))
		if err != nil || res.Appended {
			t.Fatalf("%s: Route = (%+v, %v), want hydrated (Appended=false, nil)", name, res, err)
		}
		if ids := pending(t, h, "s1"); len(ids) != 1 || ids[0] != "f-"+name {
			t.Fatalf("%s: hydration holds %v", name, ids)
		}
	}
	p, _ := newRouter(t, newCCChannel("windows", indeterminateProbe, rejecting.send))
	wantExactly(t, p.Deferral(), ErrWindowsUnsupported, "tier-2")
}

// queueSnapshot renders every key and value in the hydration namespace.
func queueSnapshot(t *testing.T, h *InboxHydration) string {
	t.Helper()
	ctx := context.Background()
	it, err := h.store.Scan(ctx, HydrationNamespace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	var b strings.Builder
	for it.Next(ctx) {
		b.WriteString(it.Key() + "=" + string(it.Value()) + "\n")
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestInboxPushWithholdsMismatchedTarget(t *testing.T) {
	p, h := newRouter(t, NewCCChannel())
	ctx := context.Background()
	control := addressedTo1("ctl")
	if _, err := p.Route(ctx, "s1", control); err != nil {
		t.Fatal(err)
	}
	if ids := queueSnapshot(t, h); !strings.Contains(ids, "ctl") || strings.Count(ids, "s1=") != 1 {
		t.Fatalf("control: the matching target did not enqueue exactly once:\n%s", ids)
	}
	before := queueSnapshot(t, h)
	for name, ev := range map[string]InboxEvent{
		"foreign session":   {ID: "w1", Class: "addressed", TargetSession: "s2", Visibility: "private"},
		"foreign task":      {ID: "w2", Class: "addressed", TargetTask: "task-of-s2", Visibility: "private"},
		"foreign scope":     {ID: "w3", Class: "addressed", TargetScope: "repo-b", OriginScope: "repo-b", Visibility: "scoped"},
		"unresolved target": {ID: "w4", Class: "addressed", Visibility: "private"},
	} {
		_, err := p.Route(ctx, "s1", ev)
		if err != ErrInboxPushWithheld {
			t.Fatalf("%s: Route err = %v, want exactly ErrInboxPushWithheld", name, err)
		}
		wantExactly(t, err, ErrInboxPushWithheld, "different session or task")
	}
	if after := queueSnapshot(t, h); after != before {
		t.Fatalf("withheld events changed the queue:\nbefore %s\nafter  %s", before, after)
	}
	if len(pending(t, h, "s2")) != 0 {
		t.Fatal("a withheld event reached another session")
	}
}

func TestInboxPushMalformedTarget(t *testing.T) {
	calls, log := 0, &frameLog{}
	p, h := newRouter(t, newCCChannel("linux", supportedProbe(fixtureHandshake(t), false, &calls), log.send))
	ctx := context.Background()
	_, err := p.Route(ctx, "bad id!", InboxEvent{ID: "m2", Class: "global-critical"})
	wantExactly(t, err, ErrHydrationTarget, "not a valid session id")
	_, err = p.Route(ctx, "", InboxEvent{ID: "m2", Class: "global-critical"})
	wantExactly(t, err, ErrHydrationTarget, "not a valid session id")
	_, err = p.Route(ctx, "s1", InboxEvent{Class: "global-critical"})
	wantExactly(t, err, ErrEnvelopeInvalid, "has no id")
	bad := addressedTo1("m4")
	bad.DataClass = provider.SensitivityTier(9)
	if _, err := p.Route(ctx, "s1", bad); err != ErrDataClassInvalid || log.count() != 0 {
		t.Fatalf("an undeclared data class routed: err=%v frames=%d", err, log.count())
	}
	// A task id is not a session id: an event naming only a task is withheld.
	task := InboxEvent{ID: "m3", Class: "addressed", TargetTask: "s1"}
	if _, err := p.Route(ctx, "s1", task); err != ErrInboxPushWithheld {
		t.Fatalf("an event naming only a task = %v, want ErrInboxPushWithheld", err)
	}
	if res, err := p.Route(ctx, "s1", addressedTo1("m5")); err != nil || !res.Appended || log.count() != 1 || len(pending(t, h, "s1")) != 0 {
		t.Fatalf("control: the matching session did not receive one direct push (%+v, %v, frames %d)", res, err, log.count())
	}
}

func TestInboxPushChannelRejected(t *testing.T) {
	calls, rejecting := 0, &frameLog{fail: true}
	ch := newCCChannel("linux", supportedProbe(fixtureHandshake(t), false, &calls), rejecting.send)
	p, h := newRouter(t, ch)
	err := ch.Deliver(context.Background(), "s1", Envelope{ID: "direct"})
	wantExactly(t, err, ErrChannelRejected, "peer closed the stream")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("rejected delivery kind = %v, want KindUnavailable", err)
	}
	res, err := p.Route(context.Background(), "s1", addressedTo1("rej-1"))
	if err != nil || res.Appended {
		t.Fatalf("Route over a rejecting channel = (%+v, %v), want hydrated (Appended=false, nil)", res, err)
	}
	if ids := pending(t, h, "s2"); len(ids) != 0 {
		t.Fatalf("a rejected delivery leaked to another session: %v", ids)
	}
	if ids := pending(t, h, "s1"); len(ids) != 1 || ids[0] != "rej-1" {
		t.Fatalf("the rejected event was lost instead of hydrated: %v", ids)
	}
}

func TestInboxPushWindowsTier2Refusal(t *testing.T) {
	calls, log := 0, &frameLog{}
	ch := newCCChannel("windows", supportedProbe(fixtureHandshake(t), false, &calls), log.send)
	p, h := newRouter(t, ch)
	err := ch.Deliver(context.Background(), "s1", Envelope{ID: "win-1"})
	wantExactly(t, err, ErrWindowsUnsupported, "not supported on Windows")
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("windows push kind = %v, want KindUnsupported", err)
	}
	wantExactly(t, p.Deferral(), ErrWindowsUnsupported, "tier-2")
	if calls != 0 || log.count() != 0 {
		t.Fatalf("windows probed %d times and sent %d frames", calls, log.count())
	}
	if res, err := p.Route(context.Background(), "s1", addressedTo1("win-2")); err != nil || res.Appended || len(pending(t, h, "s1")) != 1 {
		t.Fatalf("the windows fallback must hydrate, never skip silently: (%+v, %v)", res, err)
	}
}

func TestInboxPushFlagOffRecordsDeferral(t *testing.T) {
	ch := NewCCChannel()
	if res, err := ch.probe(context.Background(), provider.SessionToken{}); err != nil || res.status != ProbeIndeterminate {
		t.Fatalf("the production probe = (%v, %v), want indeterminate", res.status, err)
	}
	p, h := newRouter(t, ch)
	def := p.Deferral()
	wantExactly(t, def, ErrChannelUnsupported, "D-S73T1-1")
	if !cascade.HasKind(def, cascade.KindUnsupported) {
		t.Fatalf("deferral kind = %v, want KindUnsupported", def)
	}
	res, err := p.Route(context.Background(), "s1", addressedTo1("flag-off"))
	if err != nil || res.Appended {
		t.Fatalf("flag-off Route = (%+v, %v), want hydration only (Appended=false, nil)", res, err)
	}
	if ids := pending(t, h, "s1"); len(ids) != 1 || ids[0] != "flag-off" {
		t.Fatalf("hydration holds %v, want [flag-off]", ids)
	}
}

func TestInboxPushCanceled(t *testing.T) {
	p, h := newRouter(t, NewCCChannel())
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Route(canceled, "s1", addressedTo1("c1")); !cascade.HasKind(err, cascade.KindCanceled) || !strings.Contains(err.Error(), "inbox route canceled") {
		t.Fatalf("canceled Route err = %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := p.Route(context.Background(), "s1", addressedTo1("c2"))
	wantExactly(t, err, ErrInboxPushNotStarted, "not started")
	if ids := pending(t, h, "s1"); len(ids) != 0 {
		t.Fatalf("refused routes still hydrated %v", ids)
	}
	if _, err := NewInboxPush(nil, h); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewInboxPush(nil) err = %v", err)
	}
	if err := p.Start(canceled); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Start(canceled) err = %v", err)
	}
}

func TestInboxPushClosedSessionRace(t *testing.T) {
	p, h := newRouter(t, NewCCChannel())
	var wg sync.WaitGroup
	results := make([]error, 16)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = p.Route(context.Background(), "s1", addressedTo1("r"+string(rune('a'+i))))
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Stop(context.Background()) }()
	wg.Wait()
	hydrated := len(pending(t, h, "s1"))
	refused := 0
	for _, err := range results {
		if err == ErrInboxPushNotStarted {
			refused++
		} else if err != nil {
			t.Fatalf("a racing Route failed with %v", err)
		}
	}
	if hydrated+refused != len(results) {
		t.Fatalf("hydrated %d + refused %d != %d routes: an event was lost or duplicated", hydrated, refused, len(results))
	}
}

// TestPermanentHydrationError: only this package's own full and unreadable-queue failures.
func TestPermanentHydrationError(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	_ = mem.Put(ctx, HydrationNamespace, "s1", []byte("{not json"))
	h, _ := NewInboxHydration(mem, func() time.Time { return fixedNow })
	_, _, corrupt := h.AppendNextTurn(ctx, "s1")
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"full queue":       {ErrHydrationFull, true},
		"unreadable queue": {corrupt, true},
		"store quota":      {cascade.New(cascade.KindQuotaExhausted, "disk full"), false},
		"wrapped full":     {cascade.Wrap(cascade.KindUnavailable, ErrHydrationFull, "enqueue"), true},
	} {
		if got := PermanentHydrationError(tc.err); got != tc.want {
			t.Errorf("%s: PermanentHydrationError = %v, want %v", name, got, tc.want)
		}
	}
}
