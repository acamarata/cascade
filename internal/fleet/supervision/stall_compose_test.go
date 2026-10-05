package supervision

// Test fixtures for the composed stall detector (NewStallDetector): a
// map-backed session lookup, a real event bus, real stores and a frozen
// clock, plus TestNewStallDetectorValidatesConfig. No test here sleeps on
// a timer; goroutine progress is awaited by a bounded Gosched loop.

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSessions is a SessionLookup over a map, standing in for the sessions
// store in tests only.
type fakeSessions struct {
	mu      sync.Mutex
	recs    map[string]sessions.SessionRecord
	listErr error
	getErr  error
	onGet   func() // runs once, on the next Get, before it returns
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{recs: map[string]sessions.SessionRecord{}}
}

func (f *fakeSessions) put(id, state string, updatedAt int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[id] = sessions.SessionRecord{SessionID: id, State: state, UpdatedAt: updatedAt}
}

func (f *fakeSessions) Get(_ context.Context, id string) (sessions.SessionRecord, error) {
	f.mu.Lock()
	hook := f.onGet
	f.onGet = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return sessions.SessionRecord{}, f.getErr
	}
	rec, ok := f.recs[id]
	if !ok {
		return sessions.SessionRecord{}, sessions.ErrNotFound
	}
	return rec, nil
}

func (f *fakeSessions) List(context.Context, sessions.Filter) ([]sessions.SessionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]sessions.SessionRecord, 0, len(f.recs))
	for _, r := range f.recs {
		out = append(out, r)
	}
	return out, nil
}

// eventually spins on cond (yielding, never sleeping) until it holds or a
// generous wall-clock bound passes. The bound only fails a hung test.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		goruntime.Gosched()
	}
	t.Fatal("condition not met within 5s")
}

var stallT0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

const (
	testRungDelay = 10 * time.Minute
	testBudget    = 4096
)

// stallFixture is a fully composed detector over real in-memory stores.
type stallFixture struct {
	t     *testing.T
	clock *runtime.FixedClock
	bus   *events.Bus
	kvBus *storetest.MemStore
	sess  *fakeSessions
	dirs  *DirectiveStore
	jr    *journal.SQLiteStore
	attn  *Store
	req   *fakeApprovalRequester
	pub   StallPublisher
	d     *Detector
}

type fixtureOpts struct {
	hydrationOff bool
	wrapPub      func(StallPublisher) StallPublisher
}

func newStallFixture(t *testing.T, opts fixtureOpts) *stallFixture {
	t.Helper()
	clock := runtime.NewFixedClock(stallT0)
	fx := &stallFixture{t: t, clock: clock, kvBus: storetest.NewMemStore(), sess: newFakeSessions(), req: &fakeApprovalRequester{}}
	fx.bus = events.New(fx.kvBus, clock)
	fx.dirs = NewDirectiveStore(storetest.NewMemStore(), clock)
	fx.jr = journal.New(storetest.NewMemStore(), clock, "stall-compose-test")
	fx.attn = NewStore(storetest.NewMemStore(), clock, nil, sequentialIDGenerator(), 0)
	fx.pub = NewBusStallPublisher(fx.bus)
	if opts.wrapPub != nil {
		fx.pub = opts.wrapPub(fx.pub)
	}
	policy := onePerRung()
	policy.RungDelay = testRungDelay
	rungs := RungConfig{Sessions: fx.sess, Directives: fx.dirs, HydrationEnabled: !opts.hydrationOff, BudgetTokens: testBudget}
	d, err := NewStallDetector(fx.jr, rungs, fx.attn, fx.req, policy, time.Minute, clock, fx.pub)
	if err != nil {
		t.Fatalf("NewStallDetector: %v", err)
	}
	fx.d = d
	return fx
}

// markAlive pretends Run is subscribed, for tests that drive Poll directly.
func (fx *stallFixture) markAlive() { fx.d.alive.Store(true) }

func (fx *stallFixture) nowMs() int64 { return fx.clock.Now().UnixMilli() }

// escalations counts the ladder's journaled events for id.
func (fx *stallFixture) escalations(id string) int {
	fx.t.Helper()
	entries, err := fx.jr.Replay(context.Background(), id, journal.Cursor{}, []journal.Kind{journal.KindEscalation})
	if err != nil {
		fx.t.Fatalf("journal replay: %v", err)
	}
	return len(entries)
}

// stalledEvents returns every stored supervision.stalled notification ID.
func (fx *stallFixture) stalledEvents() []string {
	fx.t.Helper()
	evs, err := fx.bus.Replay(context.Background(), stalledNamespace, 0)
	if err != nil {
		fx.t.Fatalf("bus replay: %v", err)
	}
	var ids []string
	for _, ev := range evs {
		if ev.Kind != stalledKind {
			continue
		}
		p, err := events.DecodeNotificationPayload(ev.Payload)
		if err != nil {
			fx.t.Fatalf("decode: %v", err)
		}
		ids = append(ids, p.ID)
	}
	return ids
}

// sessionEvent builds a fleet.sessions.changed event for a record.
func sessionEvent(t *testing.T, rec sessions.SessionRecord, at time.Time) events.Event {
	t.Helper()
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return events.Event{Kind: sessionsChangedKind, Payload: payload, Timestamp: at}
}

// composeArgs bundles NewStallDetector's arguments so a test can break one.
type composeArgs struct {
	j         journal.Store
	rungs     RungConfig
	attn      *Store
	req       ApprovalRequester
	policy    governor.EscalationPolicy
	threshold time.Duration
	clock     runtime.Clock
	pub       StallPublisher
}

func (a composeArgs) build() (*Detector, error) {
	return NewStallDetector(a.j, a.rungs, a.attn, a.req, a.policy, a.threshold, a.clock, a.pub)
}

func goodComposeArgs(t *testing.T) composeArgs {
	t.Helper()
	clock := runtime.NewFixedClock(stallT0)
	policy := onePerRung()
	policy.RungDelay = testRungDelay
	return composeArgs{
		j:         journal.New(storetest.NewMemStore(), clock, "v"),
		rungs:     RungConfig{Sessions: newFakeSessions(), Directives: NewDirectiveStore(storetest.NewMemStore(), clock), BudgetTokens: testBudget},
		attn:      newTestStore(t, stallT0),
		req:       &fakeApprovalRequester{},
		policy:    policy,
		threshold: time.Minute,
		clock:     clock,
		pub:       NewBusStallPublisher(events.New(storetest.NewMemStore(), clock)),
	}
}

func TestNewStallDetectorValidatesConfig(t *testing.T) {
	if d, err := goodComposeArgs(t).build(); err != nil || d == nil {
		t.Fatalf("valid config: (%v, %v), want a detector", d, err)
	}
	cases := []struct {
		name  string
		unset func(*composeArgs)
	}{
		{"journal", func(a *composeArgs) { a.j = nil }},
		{"rungs.Sessions", func(a *composeArgs) { a.rungs.Sessions = nil }},
		{"rungs.Directives", func(a *composeArgs) { a.rungs.Directives = nil }},
		{"rungs.BudgetTokens", func(a *composeArgs) { a.rungs.BudgetTokens = 0 }},
		{"attention store", func(a *composeArgs) { a.attn = nil }},
		{"approval requester", func(a *composeArgs) { a.req = nil }},
		{"policy.MaxAttempts", func(a *composeArgs) { a.policy.MaxAttempts = nil }},
		{"policy.RungDelay", func(a *composeArgs) { a.policy.RungDelay = 0 }},
		{"policy.ConfidenceThreshold", func(a *composeArgs) { a.policy.ConfidenceThreshold = 0 }},
		{"threshold", func(a *composeArgs) { a.threshold = 0 }},
		{"clock", func(a *composeArgs) { a.clock = nil }},
		{"publisher", func(a *composeArgs) { a.pub = nil }},
	}
	for _, tc := range cases {
		a := goodComposeArgs(t)
		tc.unset(&a)
		d, err := a.build()
		ce, ok := err.(*cascade.Error)
		if d != nil || !ok || ce.Kind != cascade.KindInvalidInput || ce.Msg != "supervision: stall detector requires "+tc.name {
			t.Errorf("%s: got (%v, %v), want InvalidInput naming %q", tc.name, d, err, tc.name)
		}
	}
}
