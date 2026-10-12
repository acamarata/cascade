package notify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// addrTestClock is the frozen instant every addressed test starts at.
var addrTestClock = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func newAddressedService(t *testing.T) (*Service, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(addrTestClock)
	return NewService(DefaultConfig(), clock, slog.New(slog.NewTextHandler(io.Discard, nil))), clock
}

// asPrincipal binds a verified principal caller to a fresh context.
func asPrincipal(id, sessionID string) context.Context {
	return principal.WithCaller(context.Background(),
		principal.Caller{Principal: &principal.Principal{ID: id, Kind: principal.KindSession, SessionID: sessionID}})
}

// asOwnerCLI binds the owner CLI caller to a fresh context.
func asOwnerCLI() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

// deliverDrained delivers each item and drains them into the Inbox.
func deliverDrained(t *testing.T, s *Service, items ...Notification) {
	t.Helper()
	for _, n := range items {
		if err := s.Notifier.Deliver(context.Background(), n); err != nil {
			t.Fatalf("Deliver(%s): %v", n.ID, err)
		}
	}
	s.Dispatcher.Drain(context.Background())
}

func addressed(id, corr, toPrincipal, toSession string) Notification {
	return Notification{ID: id, Source: "test", Priority: PriorityNormal, Class: ClassAddressed,
		CorrelationID: corr, TargetPrincipal: toPrincipal, TargetSession: toSession, Kind: "delegation.result"}
}

func ids(ns []Notification) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

// assertSentinel checks err by identity and message, never errors.Is alone.
func assertSentinel(t *testing.T, err, want error) {
	t.Helper()
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("err = %v, want message %q", err, want.Error())
	}
	if err != want { //nolint:errorlint // identity is the assertion (cascade errors.Is compares Kind only)
		t.Fatalf("err %v carries the right message but is not the sentinel itself", err)
	}
}

func TestAddressedToPrincipalDelivered(t *testing.T) {
	s, clock := newAddressedService(t)
	deliverDrained(t, s, addressed("a-2", "job-1", "prin-a", ""))
	clock.Advance(time.Second)
	deliverDrained(t, s, addressed("a-1", "job-1", "prin-a", ""), addressed("other-job", "job-2", "prin-a", ""))
	got, err := s.ReadAddressed(asPrincipal("prin-a", "sess-a"), "job-1")
	if err != nil {
		t.Fatalf("ReadAddressed: %v", err)
	}
	if want := []string{"a-2", "a-1"}; len(got) != 2 || got[0].ID != want[0] || got[1].ID != want[1] {
		t.Fatalf("ReadAddressed = %v, want %v oldest first", ids(got), want)
	}
	if got[0].TargetPrincipal != "prin-a" || got[0].Kind != "delegation.result" {
		t.Fatalf("stored item lost its target or kind: %+v", got[0])
	}
	byID, err := s.ReadAddressedByID(asPrincipal("prin-a", "sess-a"), "a-1")
	if err != nil || byID.ID != "a-1" {
		t.Fatalf("ReadAddressedByID = %+v, %v; want a-1", byID, err)
	}
	// A session-addressed item is the caller's when it targets the caller's bound session.
	deliverDrained(t, s, addressed("s-1", "job-3", "", "sess-a"))
	got, err = s.ReadAddressed(asPrincipal("prin-a", "sess-a"), "job-3")
	if err != nil || len(got) != 1 || got[0].ID != "s-1" {
		t.Fatalf("session-addressed read = %v, %v; want [s-1]", ids(got), err)
	}
}

func TestAddressedToOwnerCLIOnlyOwner(t *testing.T) {
	s, _ := newAddressedService(t)
	deliverDrained(t, s, addressed("o-1", "job-1", OwnerCLITarget, ""))
	got, err := s.ReadAddressed(asOwnerCLI(), "job-1")
	if err != nil || len(got) != 1 || got[0].ID != "o-1" {
		t.Fatalf("owner CLI read = %v, %v; want [o-1]", ids(got), err)
	}
	for _, ctx := range []context.Context{asPrincipal("prin-a", "sess-a"), asPrincipal("prin-b", "")} {
		got, err := s.ReadAddressed(ctx, "job-1")
		if err != nil || len(got) != 0 {
			t.Fatalf("principal read of an owner-cli item = %v, %v; want empty", ids(got), err)
		}
		_, err = s.ReadAddressedByID(ctx, "o-1")
		assertSentinel(t, err, errNotAddressee)
	}
	// A principal whose ID is the owner-cli literal is refused outright.
	_, err = s.ReadAddressed(asPrincipal(OwnerCLITarget, "sess-x"), "job-1")
	assertSentinel(t, err, errNoAddressee)
	// No session view lists it either, whatever its Visibility.
	shared := addressed("o-2", "job-1", OwnerCLITarget, "")
	shared.Visibility = VisibilityShared
	deliverDrained(t, s, shared)
	if list, _ := s.Inbox.List(context.Background(), session(""), InboxOpts{}); len(list) != 0 {
		t.Fatalf("session Inbox listed principal-addressed items %v", ids(list))
	}
}

func TestAddressedNeedsExactlyOneTarget(t *testing.T) {
	s, _ := newAddressedService(t)
	ctx := context.Background()
	assertSentinel(t, s.Notifier.Deliver(ctx, addressed("both", "c", "prin-a", "sess-a")), errAddressedTargets)
	assertSentinel(t, s.Notifier.Deliver(ctx, addressed("none", "c", "", "")), errMissingScopeFields)
	scoped := Notification{ID: "scoped", Class: ClassScoped, TargetScope: "proj", TargetPrincipal: "prin-a"}
	assertSentinel(t, s.Notifier.Deliver(ctx, scoped), errAddressedTargets)
	if k, _ := cascade.KindOf(errAddressedTargets); k != cascade.KindInvalidInput {
		t.Fatalf("errAddressedTargets kind = %v, want KindInvalidInput", k)
	}
	for _, ok := range []Notification{addressed("p", "c", "prin-a", ""), addressed("s", "c", "", "sess-a")} {
		if err := s.Notifier.Deliver(ctx, ok); err != nil {
			t.Fatalf("Deliver(%s) with one target: %v", ok.ID, err)
		}
	}
}

func TestReadAddressedRefusesOtherPrincipal(t *testing.T) {
	s, _ := newAddressedService(t)
	deliverDrained(t, s, addressed("a-1", "job-1", "prin-a", ""), addressed("sa-1", "job-1", "", "sess-a"))
	// prin-b is bound to a session equal to nothing of A's principal item.
	b := asPrincipal("prin-b", "sess-b")
	_, err := s.ReadAddressedByID(b, "a-1")
	assertSentinel(t, err, errNotAddressee)
	if k, _ := cascade.KindOf(err); k != cascade.KindPermissionDenied {
		t.Fatalf("kind = %v, want KindPermissionDenied", k)
	}
	got, err := s.ReadAddressed(b, "job-1")
	if err != nil || len(got) != 0 {
		t.Fatalf("ReadAddressed(B, job-1) = %v, %v; want empty", ids(got), err)
	}
	// An empty session on the caller never matches an item's empty session.
	got, err = s.ReadAddressed(asPrincipal("prin-c", ""), "job-1")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty-session caller read %v, %v; want empty", ids(got), err)
	}
	_, err = s.ReadAddressedByID(b, "never-recorded")
	assertSentinel(t, err, ErrNotificationUnknown)
}

func TestReadAddressedRequiresCaller(t *testing.T) {
	s, _ := newAddressedService(t)
	deliverDrained(t, s, addressed("o-1", "job-1", OwnerCLITarget, ""))
	refused := map[string]context.Context{
		"no caller":    context.Background(),
		"owner view":   principal.WithCaller(context.Background(), principal.Caller{}),
		"empty ID":     asPrincipal("", "sess-a"),
		"owner-cli ID": asPrincipal(OwnerCLITarget, ""),
	}
	for name, ctx := range refused {
		_, err := s.ReadAddressed(ctx, "job-1")
		assertSentinel(t, err, errNoAddressee)
		_, err = s.ReadAddressedByID(ctx, "o-1")
		assertSentinel(t, err, errNoAddressee)
		if k, _ := cascade.KindOf(err); k != cascade.KindPermissionDenied {
			t.Fatalf("%s: kind = %v, want KindPermissionDenied", name, k)
		}
	}
}

func TestUnknownKindCarriedAndIgnored(t *testing.T) {
	s, _ := newAddressedService(t)
	known, unknown := addressed("k", "job-1", "prin-a", ""), addressed("u", "job-1", "prin-a", "")
	unknown.Kind = "vendor.never-registered.v9"
	var seen []Notification
	s.Registry.Subscribe(stubSubscriber{id: "all", matches: true, recv: func(n Notification) error {
		seen = append(seen, n)
		return nil
	}}, session("sess-a"))
	deliverDrained(t, s, known, unknown)
	got, err := s.ReadAddressed(asPrincipal("prin-a", "sess-a"), "job-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ReadAddressed = %v, %v; want both kinds", ids(got), err)
	}
	kinds := map[string]string{got[0].ID: got[0].Kind, got[1].ID: got[1].Kind}
	if kinds["u"] != unknown.Kind || kinds["k"] != known.Kind {
		t.Fatalf("kinds not carried unchanged: %v", kinds)
	}
	if len(seen) != 0 {
		t.Fatalf("a principal-addressed item reached a session subscriber: %v", ids(seen))
	}
}

func TestReadBeforeDrainReturnsNothing(t *testing.T) {
	s, clock := newAddressedService(t)
	n := addressed("a-1", "job-1", "prin-a", "")
	n.ExpiresAt = addrTestClock.Add(time.Minute)
	if err := s.Notifier.Deliver(context.Background(), n); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	a := asPrincipal("prin-a", "sess-a")
	if got, err := s.ReadAddressed(a, "job-1"); err != nil || len(got) != 0 {
		t.Fatalf("read before drain = %v, %v; want empty", ids(got), err)
	}
	s.Dispatcher.Drain(context.Background())
	if got, _ := s.ReadAddressed(a, "job-1"); len(got) != 1 {
		t.Fatalf("read after drain = %v; want [a-1]", ids(got))
	}
	clock.Advance(time.Minute)
	if got, _ := s.ReadAddressed(a, "job-1"); len(got) != 0 {
		t.Fatalf("read after expiry = %v; want empty", ids(got))
	}
	_, err := s.ReadAddressedByID(a, "a-1")
	assertSentinel(t, err, ErrNotificationExpired)
}

func TestReadAddressedCanceled(t *testing.T) {
	s, _ := newAddressedService(t)
	ctx, cancel := context.WithCancel(asOwnerCLI())
	cancel()
	_, err := s.ReadAddressed(ctx, "job-1")
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindCanceled {
		t.Fatalf("ReadAddressed on a canceled context = %v, want KindCanceled", err)
	}
}

func TestReadAddressedSkipsUnaddressedClasses(t *testing.T) {
	s, _ := newAddressedService(t)
	global := Notification{ID: "g-1", Class: ClassGlobalCritical, Priority: PriorityUrgent, CorrelationID: "job-1"}
	scoped := Notification{ID: "sc-1", Class: ClassScoped, TargetScope: "sess-a", CorrelationID: "job-1"}
	deliverDrained(t, s, global, scoped)
	for _, ctx := range []context.Context{asOwnerCLI(), asPrincipal("prin-a", "sess-a")} {
		if got, err := s.ReadAddressed(ctx, "job-1"); err != nil || len(got) != 0 {
			t.Fatalf("ReadAddressed returned unaddressed items %v, %v", ids(got), err)
		}
	}
	_, err := s.ReadAddressedByID(asOwnerCLI(), "g-1")
	assertSentinel(t, err, errNotAddressee)
}

func TestSessionInboxNeverSeesPrincipalItems(t *testing.T) {
	s, _ := newAddressedService(t)
	s.Registry.Subscribe(stubSubscriber{id: "sub", matches: true, recv: func(Notification) error { return nil }}, session("sess-b"))
	shared := addressed("p-1", "job-1", "prin-a", "")
	shared.Visibility = VisibilityShared
	deliverDrained(t, s, shared)
	list, counts := s.Inbox.List(context.Background(), session("sess-b"), InboxOpts{})
	if len(list) != 0 || counts != (Counts{}) {
		t.Fatalf("session view of a principal item: list %v counts %+v, want nothing", ids(list), counts)
	}
	_, err := s.Inbox.Read(context.Background(), session("sess-b"), "p-1")
	assertSentinel(t, err, ErrNotificationUnknown)
	assertSentinel(t, s.Inbox.Ack(context.Background(), session("sess-b"), "p-1"), ErrNotificationUnknown)
}

func TestReadAddressedClassGuard(t *testing.T) {
	s, _ := newAddressedService(t)
	scoped := Notification{ID: "sc-2", Class: ClassScoped, TargetScope: "proj-x", TargetSession: "sess-a", CorrelationID: "job-1"}
	global := Notification{ID: "g-2", Class: ClassGlobalCritical, TargetSession: "sess-a", CorrelationID: "job-1"}
	deliverDrained(t, s, scoped, global)
	a := asPrincipal("prin-a", "sess-a")
	if got, err := s.ReadAddressed(a, "job-1"); err != nil || len(got) != 0 {
		t.Fatalf("ReadAddressed returned non-addressed items that name the caller's session: %v, %v", ids(got), err)
	}
	for _, id := range []string{"sc-2", "g-2"} {
		_, err := s.ReadAddressedByID(a, id)
		assertSentinel(t, err, errNotAddressee)
	}
}

func TestReusedIDCannotReaddress(t *testing.T) {
	s, _ := newAddressedService(t)
	deliverDrained(t, s, addressed("x", "job-1", "prin-a", ""))
	deliverDrained(t, s, addressed("x", "job-1", "prin-b", ""))
	got, err := s.ReadAddressedByID(asPrincipal("prin-a", "sess-a"), "x")
	if err != nil || got.TargetPrincipal != "prin-a" {
		t.Fatalf("addressee lost its item to a reused id: %+v, %v", got, err)
	}
	_, err = s.ReadAddressedByID(asPrincipal("prin-b", "sess-b"), "x")
	assertSentinel(t, err, errNotAddressee)
	stale := addressed("x", "job-1", "prin-b", "")
	stale.ExpiresAt = addrTestClock.Add(-time.Minute)
	deliverDrained(t, s, stale)
	if got, err := s.ReadAddressedByID(asPrincipal("prin-a", "sess-a"), "x"); err != nil || got.TargetPrincipal != "prin-a" {
		t.Fatalf("an expired reused id expired the addressee's item: %+v, %v", got, err)
	}
}
