package notify

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
)

// awayRaceIterations is high enough that an interleaving at the return edge is
// hit repeatedly rather than by luck. Each iteration is a fresh controller, so
// a single inconsistent observation fails the whole test.
const awayRaceIterations = 300

// TestAwayConcurrentTickAndActivityNeverStrandAccumulation is the invariant
// -race alone cannot see, because both paths are individually mutex-guarded:
// the state transition and the accumulation flip must be ONE atomic step. If
// they are not, a Tick that opens an away episode and a concurrent activity
// event that closes it can leave accumulation on with the machine Active -- a
// permanent notification blackhole, since only an Away->Active edge ever turns
// accumulation off again.
//
// The load-bearing assertion is the sampler: it reads the pair through
// Snapshot, under the very lock every transition holds, so a flip performed
// outside that lock is observable as a state/flag combination the machine
// never legitimately occupies. Asserting only the settled state after both
// goroutines finish is NOT enough -- it passes against a controller that
// flips the flag outside the lock, because the last writer usually still wins.
func TestAwayConcurrentTickAndActivityNeverStrandAccumulation(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < awayRaceIterations; i++ {
		h := newTestAway(t, newFixedClockPtr(time.Unix(int64(20000+i*3600), 0)), stubAdmission{}, "node-race")
		// One Tick short of Away: the next Tick opens the episode.
		h.clock.Advance(11 * time.Minute)
		h.ac.Tick(ctx)

		bad := runAwayReturnRace(ctx, h)
		if bad != "" {
			t.Fatalf("iteration %d: %s", i, bad)
		}
		if state, accumulating := h.ac.Snapshot(); (state == StateAway) != accumulating {
			t.Fatalf("iteration %d: settled state %v with accumulating=%v", i, state, accumulating)
		}
		assertJournalIntentBeforeAck(t, h.journal, "node-race", i)
	}
}

// runAwayReturnRace drives one Tick and one activity event concurrently while a
// third goroutine hammers Snapshot, and returns a description of the first
// inconsistent observation, or "" when every observation held.
func runAwayReturnRace(ctx context.Context, h *awayHarness) string {
	start := make(chan struct{})
	done := make(chan struct{})
	var drivers, sampler sync.WaitGroup
	var mu sync.Mutex
	bad := ""

	drivers.Add(2)
	go func() {
		defer drivers.Done()
		<-start
		h.ac.Tick(ctx)
	}()
	go func() {
		defer drivers.Done()
		<-start
		h.ac.HandleEvent(ctx, ActivityEvent{At: h.clock.Now()})
	}()

	sampler.Add(1)
	go func() {
		defer sampler.Done()
		<-start
		for {
			state, accumulating := h.ac.Snapshot()
			if (state == StateAway) != accumulating {
				mu.Lock()
				bad = "observed state " + state.String() + " with accumulating=" + boolWord(accumulating) +
					" -- the accumulation flip is not inside the state transition's critical section"
				mu.Unlock()
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	close(start)
	drivers.Wait()
	close(done)
	sampler.Wait()
	mu.Lock()
	defer mu.Unlock()
	return bad
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// assertJournalIntentBeforeAck proves the journal for entityID is replayable:
// every episode's KindIntent is recorded BEFORE its KindAck. An Ack landing
// first makes ReplayState report Away forever, because the Ack cancels an
// episode that is only opened afterwards.
func assertJournalIntentBeforeAck(t *testing.T, j journal.Store, entityID string, iteration int) {
	t.Helper()
	entries, err := j.Replay(context.Background(), entityID, journal.Cursor{},
		[]journal.Kind{journal.KindIntent, journal.KindAck})
	if err != nil {
		t.Fatalf("iteration %d: Replay: %v", iteration, err)
	}
	intentAt := map[string]int{}
	for idx, e := range entries {
		switch e.Kind {
		case journal.KindIntent:
			intentAt[e.OperationID] = idx
		case journal.KindAck:
			at, ok := intentAt[e.OperationID]
			if !ok {
				t.Fatalf("iteration %d: KindAck for episode %q with no preceding KindIntent", iteration, e.OperationID)
			}
			if at >= idx {
				t.Fatalf("iteration %d: episode %q journalled Ack at %d before Intent at %d", iteration, e.OperationID, idx, at)
			}
		case journal.KindCheckpoint, journal.KindEscalation, journal.KindResumeCursor,
			journal.KindFanOutLegStarted, journal.KindFanOutLegDone, journal.KindNodeStream:
			t.Fatalf("iteration %d: journal kind %v past Replay's Intent/Ack filter", iteration, e.Kind)
		}
	}
}

// journalTransitionProbe wraps a real journal.Store and records, at the exact
// instant the controller appends a transition entry, whether accumulation was
// already flipped. This is the deterministic half of the atomicity proof, and
// the one that actually bites: the controller appends INSIDE the same critical
// section that changes state and flips the flag, so an Away-entry (KindIntent)
// must already observe accumulating=true and an Active-return (KindAck) must
// already observe accumulating=false. Flipping the flag after the unlock --
// even if the settled end state looks right -- is observable here, whereas a
// concurrent sampler can miss it because the unlocking goroutine usually keeps
// the CPU long enough to finish the flip.
type journalTransitionProbe struct {
	journal.Store
	router *NotificationRouter

	mu      sync.Mutex
	samples []journalProbeSample
}

type journalProbeSample struct {
	kind         journal.Kind
	accumulating bool
	// buffered is how many notifications were still in the accumulation
	// buffer at this append instant. On the Active-return append it must be
	// 0: the return transition drains inside the same critical section.
	buffered int
}

func (p *journalTransitionProbe) Append(ctx context.Context, entityID string, kind journal.Kind, operationID string, payload json.RawMessage) (journal.Entry, error) {
	p.mu.Lock()
	p.samples = append(p.samples, journalProbeSample{
		kind:         kind,
		accumulating: p.router.Accumulating(),
		buffered:     p.router.queues.accumulatedLen(),
	})
	p.mu.Unlock()
	return p.Store.Append(ctx, entityID, kind, operationID, payload)
}

func (p *journalTransitionProbe) taken() []journalProbeSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]journalProbeSample, len(p.samples))
	copy(out, p.samples)
	return out
}

// TestAwayTransitionFlipsAccumulationInsideItsCriticalSection is the
// deterministic atomicity proof described on journalTransitionProbe.
func TestAwayTransitionFlipsAccumulationInsideItsCriticalSection(t *testing.T) {
	ctx := context.Background()
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(30000, 0)), stubAdmission{}, "node-atomic")
	probe := &journalTransitionProbe{Store: h.journal, router: h.router}
	h.deps.Journal = probe
	h.ac = NewAwayController(h.deps, h.cfg)

	h.driveToAway(t)
	h.ac.HandleEvent(ctx, ActivityEvent{At: h.clock.Now()})

	samples := probe.taken()
	if len(samples) != 2 {
		t.Fatalf("journal appends = %d, want the Intent/Ack pair", len(samples))
	}
	if samples[0].kind != journal.KindIntent || !samples[0].accumulating {
		t.Fatalf("at the Away-entry append: kind=%v accumulating=%v, want KindIntent with accumulation ALREADY on "+
			"(the flip is outside the transition's critical section)", samples[0].kind, samples[0].accumulating)
	}
	if samples[1].kind != journal.KindAck || samples[1].accumulating {
		t.Fatalf("at the Active-return append: kind=%v accumulating=%v, want KindAck with accumulation ALREADY off "+
			"(the flip is outside the transition's critical section)", samples[1].kind, samples[1].accumulating)
	}
}

// TestAwayReturnDrainsTheBufferInsideItsCriticalSection is the F1 finding: the
// return transition must SNAPSHOT the accumulated items in the same critical
// section that flips accumulation off, not leave them for a Compile that
// re-reads the live gate after the unlock. The input is the one that breaks
// the latter: a stale-but-newer presence reading (fresher than the last
// recorded activity, yet still older than the threshold) returns the
// controller to Active, and the very next two Ticks re-enter Away — at which
// point a gate-reading Compile refuses the digest of an episode that already
// ended, and AC#4 fails there with nothing but an ERROR log.
//
// The load-bearing assertion is the probe's buffered count at the KindAck
// append: that append happens inside the return transition's own critical
// section, so a buffer still holding the episode's items there proves the
// drain is outside it. Asserting only that the digest arrived is NOT enough —
// sequentially it arrives either way, because the refusal needs the re-entry
// to land in the unlocked window.
func TestAwayReturnDrainsTheBufferInsideItsCriticalSection(t *testing.T) {
	ctx := context.Background()
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(40000, 0)), stubAdmission{}, "node-drain")
	probe := &journalTransitionProbe{Store: h.journal, router: h.router}
	h.deps.Journal = probe
	h.ac = NewAwayController(h.deps, h.cfg)
	sub, got := recordingSubscriber("s1", true)
	h.registry.Subscribe(sub, session("s1", "proj-1"))

	h.driveToAway(t)
	episodeA := h.ac.EpisodeID()
	routeBusNotification(h.router, "backup.result", "n1", "proj-1")

	// Stale-but-newer: 10 minutes ago is after the last recorded activity
	// (11 minutes ago) but not inside the 10-minute threshold.
	h.ac.HandleEvent(ctx, ActivityEvent{At: h.clock.Now().Add(-10 * time.Minute)})

	samples := probe.taken()
	if len(samples) != 2 {
		t.Fatalf("journal appends = %d, want the Intent/Ack pair", len(samples))
	}
	if samples[1].kind != journal.KindAck || samples[1].buffered != 0 {
		t.Fatalf("at the Active-return append: kind=%v buffered=%d, want KindAck with the buffer ALREADY drained "+
			"(the drain is outside the transition's critical section, so a re-entry can refuse this episode's digest)",
			samples[1].kind, samples[1].buffered)
	}

	// Away re-enters immediately on the next two Ticks, under a NEW episode.
	h.ac.Tick(ctx)
	if state := h.ac.Tick(ctx); state != StateAway {
		t.Fatalf("Tick after a stale-but-newer return = %v, want an immediate re-entry to StateAway", state)
	}
	if reentered := h.ac.EpisodeID(); reentered == "" || reentered == episodeA {
		t.Fatalf("re-entered episode id = %q, want a new episode distinct from %q", reentered, episodeA)
	}

	// Episode A's digest was still delivered, exactly once, for episode A.
	NewDispatcher(h.router.queues, h.registry, NewInbox(), h.clock, silentLogger()).Drain(ctx)
	if len(*got) != 1 {
		t.Fatalf("subscriber got %d notifications, want exactly episode A's one digest", len(*got))
	}
	if d := (*got)[0]; d.CorrelationID != episodeA || d.ID != episodeA+":s1" {
		t.Fatalf("delivered digest = ID:%q CorrelationID:%q, want episode A (%q)", d.ID, d.CorrelationID, episodeA)
	}
}
