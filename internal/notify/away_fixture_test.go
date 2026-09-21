package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// stubAdmission is a permanently-idle AdmissionIdleSource (not a Mock*/
// Fake*/Noop* symbol; Art.1 permits any test double under _test.go).
type stubAdmission struct {
	inflight int
	queued   int
}

func (s stubAdmission) Inflight() int   { return s.inflight }
func (s stubAdmission) QueueDepth() int { return s.queued }

// admissionBox lets a test flip the machine-idle signal between Tick calls.
type admissionBox struct {
	mu   sync.Mutex
	busy bool
}

func (a *admissionBox) setBusy(busy bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.busy = busy
}

func (a *admissionBox) Inflight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return 1
	}
	return 0
}

func (a *admissionBox) QueueDepth() int { return 0 }

// presenceBox is a test PresenceSource: the composition root's real
// implementation lands with the wiring ticket, so the controller's presence
// input is exercised here through its own injected interface.
type presenceBox struct {
	mu sync.Mutex
	at time.Time
}

func (p *presenceBox) LastActivity() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.at
}

func (p *presenceBox) set(at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.at = at
}

// stallBox is a test StallSource holding notices until the controller drains
// them, exactly once each.
type stallBox struct {
	mu      sync.Mutex
	pending []StallNotice
}

func (s *stallBox) DrainStallNotices() []StallNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

func (s *stallBox) push(notices ...StallNotice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, notices...)
}

// remaining reports how many notices the source still holds, which is how a
// test tells "consumed" from "left pending for the next away episode".
func (s *stallBox) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// fixedClockPtr is a mutable, goroutine-safe clock local to these tests so
// elapsed-time assertions can advance it between calls (fixedClock in
// notify_test.go is a value type with no Advance).
type fixedClockPtr struct {
	mu  sync.Mutex
	now time.Time
}

func newFixedClockPtr(at time.Time) *fixedClockPtr { return &fixedClockPtr{now: at} }

func (c *fixedClockPtr) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClockPtr) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// awayHarness is one fully-wired away-mode fixture: a real queueSet, a real
// NotificationRouter and Notifier over it, a real journal.Store (over
// storetest.NewMemStore, the same real-counterpart fixture router_test.go
// uses), a real DigestCompiler, and the controller under test.
type awayHarness struct {
	ac       *AwayController
	deps     AwayDeps
	cfg      AwayConfig
	router   *NotificationRouter
	registry *Registry
	notifier *Notifier
	journal  journal.Store
	presence *presenceBox
	stalls   *stallBox
	clock    *fixedClockPtr
}

// newAwayFixture builds every collaborator but not the controller, so a
// restart test can construct one with NewAwayControllerFrom over the SAME
// journal.
func newAwayFixture(t *testing.T, clock *fixedClockPtr, admission AdmissionIdleSource, entityID string) *awayHarness {
	t.Helper()
	queues := newQueueSet(DefaultConfig(), silentLogger())
	registry := NewRegistry()
	router := NewNotificationRouter(queues, clock, silentLogger())
	notifier := NewNotifier(queues, clock)
	j := journal.New(storetest.NewMemStore(), clock, journal.DefaultNamespace)
	cfg := AwayConfig{Threshold: 10 * time.Minute, DigestUrgentDeepLinks: 10}
	h := &awayHarness{
		cfg: cfg, router: router, registry: registry, notifier: notifier, journal: j,
		presence: &presenceBox{}, stalls: &stallBox{}, clock: clock,
	}
	h.deps = AwayDeps{
		Router: router, Digest: NewDigestCompiler(registry, notifier, router, clock, cfg, silentLogger()),
		Presence: h.presence, Stalls: h.stalls, Admission: admission, Clock: clock,
		Journal: j, EntityID: entityID, Log: silentLogger(),
	}
	return h
}

func newTestAway(t *testing.T, clock *fixedClockPtr, admission AdmissionIdleSource, entityID string) *awayHarness {
	t.Helper()
	h := newAwayFixture(t, clock, admission, entityID)
	h.ac = NewAwayController(h.deps, h.cfg)
	return h
}

// driveToAway advances past the threshold and Ticks twice with the machine
// side idle throughout, the shortest compliant path to Away: no Tick in the
// window observed a busy admission, so the confirming window held.
func (h *awayHarness) driveToAway(t *testing.T) {
	t.Helper()
	h.clock.Advance(11 * time.Minute)
	h.ac.Tick(context.Background())
	if got := h.ac.Tick(context.Background()); got != StateAway {
		t.Fatalf("driveToAway: second Tick = %v, want StateAway", got)
	}
	if !h.router.Accumulating() {
		t.Fatal("driveToAway: accumulation not on after entering Away")
	}
}
