package notify

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// newAwayFixtureOn builds a fixture whose journal lives on an existing
// store, so a "restart" test can reconstruct a controller over the SAME
// durable journal a killed process wrote to.
func newAwayFixtureOn(t *testing.T, st provider.Store, clock *fixedClockPtr, admission AdmissionIdleSource, entityID string) *awayHarness {
	t.Helper()
	h := newAwayFixture(t, clock, admission, entityID)
	h.journal = journal.New(st, clock, journal.DefaultNamespace)
	h.deps.Journal = h.journal
	return h
}

// TestAwayJournalReplay is the ticket's named required check: an Away-entry
// journalled with no matching Active-return (kill -9 mid-away) replays as
// StateAway with the same episode id; a full Intent+Ack pair replays Active.
func TestAwayJournalReplay(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(6000, 0)), stubAdmission{}, "node-kill9")
	h.driveToAway(t)
	episodeID := h.ac.EpisodeID()

	got, err := ReplayState(context.Background(), h.journal, "node-kill9")
	if err != nil {
		t.Fatalf("ReplayState: %v", err)
	}
	if got.State != StateAway {
		t.Fatalf("ReplayState after kill -9 mid-away = %v, want StateAway", got.State)
	}
	if got.EpisodeID != episodeID || episodeID == "" {
		t.Fatalf("ReplayState episode = %q, want the open episode %q", got.EpisodeID, episodeID)
	}

	h.ac.HandleEvent(context.Background(), ActivityEvent{At: h.clock.Now()})
	after, err := ReplayState(context.Background(), h.journal, "node-kill9")
	if err != nil {
		t.Fatalf("ReplayState after return: %v", err)
	}
	if after.State != StateActive || after.EpisodeID != "" {
		t.Fatalf("ReplayState after an Intent+Ack pair = %+v, want Active with no episode", after)
	}
}

// TestAwayReplayResumesAwayAndWritesTheAck is the input a replayed state used
// to have nowhere to go: kill -9 with five notifications accumulated, then a
// restart that reconstructs the controller from the journal. The resumed
// controller must be Away with accumulation ON before its first Tick, and its
// eventual return must write the KindAck against the SAME episode the
// pre-restart KindIntent opened.
func TestAwayReplayResumesAwayAndWritesTheAck(t *testing.T) {
	ctx := context.Background()
	st := storetest.NewMemStore()
	clock := newFixedClockPtr(time.Unix(6200, 0))

	before := newAwayFixtureOn(t, st, clock, stubAdmission{}, "node-restart")
	before.ac = NewAwayController(before.deps, before.cfg)
	before.driveToAway(t)
	for i := 0; i < 5; i++ {
		routeBusNotification(before.router, "backup.result", "pre-"+strconv.Itoa(i), "proj-1")
	}
	if buf := len(before.router.queues.accumBuf); buf != 5 {
		t.Fatalf("setup: %d accumulated before the kill, want 5", buf)
	}
	episodeID := before.ac.EpisodeID()

	// kill -9: the process, its router and its in-memory buffer are gone; the
	// journal on st is all that survives.
	restored, err := ReplayState(ctx, before.journal, "node-restart")
	if err != nil {
		t.Fatalf("ReplayState: %v", err)
	}
	after := newAwayFixtureOn(t, st, clock, stubAdmission{}, "node-restart")
	after.ac = NewAwayControllerFrom(after.deps, after.cfg, restored)

	if after.ac.State() != StateAway || after.ac.EpisodeID() != episodeID {
		t.Fatalf("restored controller = (%v, %q), want (StateAway, %q)",
			after.ac.State(), after.ac.EpisodeID(), episodeID)
	}
	if !after.router.Accumulating() {
		t.Fatal("restored Away controller is not accumulating before its first Tick")
	}
	// The journal records transitions, not buffer contents: the resumed
	// episode starts with an empty buffer, by design and recorded as such.
	if buf := len(after.router.queues.accumBuf); buf != 0 {
		t.Fatalf("restored buffer holds %d items, want 0", buf)
	}

	sub, got := recordingSubscriber("s1", true)
	after.registry.Subscribe(sub, session("s1", "proj-1"))
	routeBusNotification(after.router, "backup.result", "post-1", "proj-1")
	after.ac.HandleEvent(ctx, ActivityEvent{At: clock.Now()})

	final, err := ReplayState(ctx, after.journal, "node-restart")
	if err != nil {
		t.Fatalf("ReplayState after the resumed return: %v", err)
	}
	if final.State != StateActive {
		t.Fatalf("ReplayState after the resumed return = %v, want StateActive (the Ack landed)", final.State)
	}
	NewDispatcher(after.router.queues, after.registry, NewInbox(), clock, silentLogger()).Drain(ctx)
	if len(*got) != 1 || (*got)[0].CorrelationID != episodeID {
		t.Fatalf("resumed digest = %+v, want exactly one correlated to episode %q", *got, episodeID)
	}
}

// TestAwayReplayStateReturnsTheLastUnmatchedIntent is the refused input: two
// kills leave two open episodes, and the newest is the live one. Returning
// the first would resurrect a stale episode forever.
func TestAwayReplayStateReturnsTheLastUnmatchedIntent(t *testing.T) {
	ctx := context.Background()
	clock := newFixedClockPtr(time.Unix(6300, 0))
	j := journal.New(storetest.NewMemStore(), clock, journal.DefaultNamespace)
	for _, id := range []string{"episode-A", "episode-B"} {
		if _, err := j.Append(ctx, "node-twice", journal.KindIntent, id, json.RawMessage(`{"episode_id":"`+id+`"}`)); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}

	got, err := ReplayState(ctx, j, "node-twice")
	if err != nil {
		t.Fatalf("ReplayState: %v", err)
	}
	if got.State != StateAway || got.EpisodeID != "episode-B" {
		t.Fatalf("ReplayState = %+v, want the LAST unmatched Intent (episode-B)", got)
	}
}

// TestAwayReplayStateNoEntriesIsActive covers the "no entries at all" branch
// distinctly from "a matched pair".
func TestAwayReplayStateNoEntriesIsActive(t *testing.T) {
	clock := newFixedClockPtr(time.Unix(6100, 0))
	j := journal.New(storetest.NewMemStore(), clock, journal.DefaultNamespace)
	got, err := ReplayState(context.Background(), j, "never-away")
	if err != nil {
		t.Fatalf("ReplayState: %v", err)
	}
	if got.State != StateActive || got.EpisodeID != "" {
		t.Fatalf("ReplayState with no entries = %+v, want Active with no episode", got)
	}
}

// TestAwayRestoreRefusesAnAnonymousAwayEpisode is the fail-closed control on
// the restore seam: a replayed Away state with no episode id cannot be closed
// later, so it must not resume accumulating.
func TestAwayRestoreRefusesAnAnonymousAwayEpisode(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(6400, 0)), stubAdmission{}, "node-anon")
	h.ac = NewAwayControllerFrom(h.deps, h.cfg, ReplayResult{State: StateAway})
	if h.ac.State() != StateActive {
		t.Fatalf("State() = %v after restoring Away with no episode id, want StateActive", h.ac.State())
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation resumed for an episode that can never be closed")
	}
}

// TestAwayRestoreResumesAwayPending covers the remaining restore branch.
func TestAwayRestoreResumesAwayPending(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(6500, 0)), stubAdmission{}, "node-pending")
	h.ac = NewAwayControllerFrom(h.deps, h.cfg, ReplayResult{State: StateAwayPending})
	if h.ac.State() != StateAwayPending {
		t.Fatalf("State() = %v, want StateAwayPending", h.ac.State())
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation on while only AwayPending")
	}
	// The backdated idle window means the very next Tick can confirm Away.
	if got := h.ac.Tick(context.Background()); got != StateAway {
		t.Fatalf("Tick on a resumed AwayPending = %v, want StateAway", got)
	}
}

// TestAwayConfigValidate covers AwayConfig.Validate's two refusal paths plus
// the accept path, and DefaultAwayConfig's literal contract values. Validation
// is proven on the struct: it is what a hot reload must run before applying.
func TestAwayConfigValidate(t *testing.T) {
	def := DefaultAwayConfig()
	if def.Threshold != 30*time.Minute || def.DigestUrgentDeepLinks != 10 {
		t.Fatalf("DefaultAwayConfig() = %+v, want {30m 10}", def)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate() on defaults: %v", err)
	}
	if err := (AwayConfig{Threshold: 0, DigestUrgentDeepLinks: 1}).Validate(); err == nil {
		t.Fatal("Validate() accepted a zero threshold")
	}
	if err := (AwayConfig{Threshold: time.Minute, DigestUrgentDeepLinks: -1}).Validate(); err == nil {
		t.Fatal("Validate() accepted a negative digest_urgent_deeplinks")
	}
}

// TestAwayAdmissionSourceCompileTimeMatch is a cheap positive control on the
// compile-time assertion in away_config.go: a value satisfying only
// Inflight/QueueDepth must be assignable to AdmissionIdleSource.
func TestAwayAdmissionSourceCompileTimeMatch(_ *testing.T) {
	var _ AdmissionIdleSource = stubAdmission{}
}
