// Purpose: the weekly digest under test — that its window is explicit and
//
//	honest, that identical input produces identical bytes, that it reports
//	promotions inside the window and only those, that it publishes exactly
//	once per fire (empty weeks included), that building one leaves the
//	store byte-identical, and the redaction canary: no machine path and no
//	secret-shaped value from a candidate's content may reach the rendered
//	payload.
//
// SPORT: event:MemoryWeeklyDigest (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
)

// recordingDigestSink captures every digest published to it.
type recordingDigestSink struct {
	digests  []memory.MemoryWeeklyDigest
	failWith error
}

func (s *recordingDigestSink) MemoryWeeklyDigestReady(
	_ context.Context, ev memory.MemoryWeeklyDigest,
) error {
	s.digests = append(s.digests, ev)
	return s.failWith
}

// window returns the standard one-week window ending at the frozen now.
func window() DigestWindow {
	return DigestWindow{Since: fixedNow.Add(-DefaultDigestCadence), Until: fixedNow}
}

func TestDigestReportsTheWindowItSpeaksFor(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")

	got, err := f.queue.Digest(context.Background(), window())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	if !got.Since.Equal(fixedNow.Add(-DefaultDigestCadence)) || !got.Until.Equal(fixedNow) {
		t.Errorf("window = %s..%s, want it stated explicitly", got.Since, got.Until)
	}
	if got.MinRefCount != memory.PromotionMinRefCount || got.MinSessions != memory.PromotionMinSessions {
		t.Errorf("thresholds = %d/%d, want them carried so a reader can check "+
			"the below-threshold claim", got.MinRefCount, got.MinSessions)
	}
	if len(got.Pending) != 1 || got.Pending[0].ID != "project/below" {
		t.Errorf("pending = %+v", got.Pending)
	}
}

// TestDigestReportsOnlyPromotionsInsideTheWindow is the difference between
// a weekly digest and a running total.
func TestDigestReportsOnlyPromotionsInsideTheWindow(t *testing.T) {
	f := newFixture(t)
	// An old promotion, made a fortnight before the window opens.
	f.clock.Set(fixedNow.Add(-14 * 24 * time.Hour))
	f.promote(t, memory.KindUser, "old")
	// A promotion inside the window.
	f.clock.Set(fixedNow.Add(-2 * 24 * time.Hour))
	f.promote(t, memory.KindProject, "recent")
	f.clock.Set(fixedNow)

	got, err := f.queue.Digest(context.Background(), window())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	if len(got.Promoted) != 1 || got.Promoted[0].ID != "project/recent" {
		t.Fatalf("promoted = %+v, want only the promotion inside the window", got.Promoted)
	}
	// The old promotion is still visible in the queue itself, which is
	// where a revert is decided; it is simply not this week's news.
	if listed := f.list(t, ListParams{}); len(listed.Promoted) != 2 {
		t.Errorf("the listing shows %v, want both promotions still reviewable",
			ids(listed.Promoted))
	}
}

func TestDigestIsDeterministicAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	f.observe(t, memory.KindFeedback, "another", "s-2")
	f.promote(t, memory.KindUser, "standing")
	before := treeDigest(t, f.base)

	first, err := f.queue.Digest(context.Background(), window())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	second, err := f.queue.Digest(context.Background(), window())
	if err != nil {
		t.Fatalf("Digest again: %v", err)
	}

	firstBytes := mustMarshal(t, first)
	if secondBytes := mustMarshal(t, second); string(firstBytes) != string(secondBytes) {
		t.Fatalf("two digests of the same store differ:\n%s\n%s", firstBytes, secondBytes)
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("building a digest changed the store: %s -> %s", before, after)
	}
}

func TestDigestRefusesABackwardsWindow(t *testing.T) {
	f := newFixture(t)
	_, err := f.queue.Digest(context.Background(),
		DigestWindow{Since: fixedNow, Until: fixedNow.Add(-time.Hour)})
	if !errors.Is(err, ErrInvalidDigestWindow) {
		t.Fatalf("err = %v, want ErrInvalidDigestWindow", err)
	}
	_, err = f.queue.Digest(context.Background(),
		DigestWindow{Since: fixedNow.Add(time.Hour), Until: fixedNow.Add(2 * time.Hour)})
	if !errors.Is(err, ErrInvalidDigestWindow) {
		t.Fatalf("a window opening after the read instant: err = %v, want ErrInvalidDigestWindow", err)
	}
}

// TestDigestCarriesNoContentFromTheCandidatesItReports is hard
// requirement 3's canary, asserted on the RENDERED bytes — the exact thing
// that leaves the process on the event bus.
func TestDigestCarriesNoContentFromTheCandidatesItReports(t *testing.T) {
	const (
		pathCanary   = "/Users/a-real-person/Library/cascade/memory/secret-place"
		secretCanary = "sk-live-0123456789abcdefghijklmnop"
	)
	f := newFixture(t)
	poisoned := draft(memory.KindProject, "canary")
	poisoned.Description = "see " + pathCanary
	poisoned.Body = "token " + secretCanary + "\n"
	if _, err := f.ledger.Observe(context.Background(),
		memory.Observation{SessionID: "s-" + secretCanary, Draft: poisoned}); err != nil {
		t.Fatalf("observe the poisoned draft: %v", err)
	}
	// A damaged candidate too: its refusal names a machine path, and the
	// digest must carry the address without the diagnostic.
	damaged := filepath.Join(f.base, "candidates", "project", "damaged.candidate.json")
	if err := os.WriteFile(damaged, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing a damaged candidate: %v", err)
	}

	got, err := f.queue.Digest(context.Background(), window())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	rendered := string(mustMarshal(t, got))

	for _, canary := range []string{pathCanary, secretCanary, "a body for canary"} {
		if strings.Contains(rendered, canary) {
			t.Errorf("the digest leaked %q:\n%s", canary, rendered)
		}
	}
	if !strings.Contains(rendered, "project/canary") {
		t.Errorf("the digest dropped the candidate entirely:\n%s", rendered)
	}
	if len(got.Unreadable) != 1 || got.Unreadable[0] != "project/damaged" {
		t.Errorf("unreadable = %v, want the address alone", got.Unreadable)
	}
	if strings.Contains(rendered, f.base) {
		t.Errorf("the digest leaked the store path:\n%s", rendered)
	}
}

func TestDigestJobPublishesExactlyOncePerFireIncludingQuietWeeks(t *testing.T) {
	f := newFixture(t)
	sink := &recordingDigestSink{}
	job := NewDigestJob(f.queue, 0, sink)

	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("first fire: %v", err)
	}
	if len(sink.digests) != 1 {
		t.Fatalf("an empty week published %d digests, want exactly 1 — a weekly "+
			"event that appears only when there is news cannot be told from a "+
			"job that stopped running", len(sink.digests))
	}
	if got := sink.digests[0]; !got.Until.Equal(fixedNow) ||
		!got.Since.Equal(fixedNow.Add(-DefaultDigestCadence)) {
		t.Errorf("window = %s..%s, want the default cadence ending now", got.Since, got.Until)
	}

	f.observe(t, memory.KindProject, "below", "s-1")
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("second fire: %v", err)
	}
	if len(sink.digests) != 2 {
		t.Fatalf("digests = %d, want one per fire", len(sink.digests))
	}
	if ids(sink.digests[1].Pending) == nil || sink.digests[1].Pending[0].ID != "project/below" {
		t.Errorf("second digest pending = %+v", sink.digests[1].Pending)
	}
}

func TestDigestJobReportsASinkFailure(t *testing.T) {
	f := newFixture(t)
	sink := &recordingDigestSink{failWith: errors.New("the bus refused it")}
	if _, err := NewDigestJob(f.queue, DefaultDigestCadence, sink).Run(context.Background()); err == nil {
		t.Fatal("a fire nobody received was reported as a success")
	}
}

func TestDigestJobWithNoSinkStillRuns(t *testing.T) {
	f := newFixture(t)
	got, err := NewDigestJob(f.queue, -time.Hour, nil).Run(context.Background())
	if err != nil {
		t.Fatalf("no-sink fire: %v", err)
	}
	if !got.Until.Equal(fixedNow) {
		t.Errorf("Until = %s, want the injected clock's now", got.Until)
	}
}

// mustMarshal renders a value the way the composition root publishes it.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return data
}
