// Purpose: the review queue's read path under test — what the listing
//
//	shows, what it refuses to show, what it must never silently omit, and
//	the proof that reading is not deciding (a listing leaves the store
//	byte-identical). Also holds FuzzReviewRPCParams, the §5.7 target over
//	this package's external-input decoder. The fixture lives in
//	fixture_test.go.
//
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestListSeparatesBelowThresholdFromPromoted(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	f.promote(t, memory.KindUser, "standing")

	got := f.list(t, ListParams{})

	if want := []string{"project/below"}; !equalStrings(ids(got.Pending), want) {
		t.Errorf("pending = %v, want %v", ids(got.Pending), want)
	}
	if want := []string{"user/standing"}; !equalStrings(ids(got.Promoted), want) {
		t.Errorf("promoted = %v, want %v", ids(got.Promoted), want)
	}
	if got.MinRefCount != memory.PromotionMinRefCount || got.MinSessions != memory.PromotionMinSessions {
		t.Errorf("thresholds = %d/%d, want the Q-6 values", got.MinRefCount, got.MinSessions)
	}
	if !got.At.Equal(fixedNow) {
		t.Errorf("At = %s, want the injected clock's now", got.At)
	}
	if got.Pending[0].RefCount != 1 || got.Pending[0].Sessions != 1 {
		t.Errorf("pending row = %+v, want the evidence a reader can check", got.Pending[0])
	}
}

// TestListNeverPresentsAnAboveThresholdCandidateAsAwaitingReview pins the
// division of labour: the mechanical lane owns a candidate that has
// crossed the threshold. It must not appear as a review item — and it must
// not vanish either, which is what the separate section is for.
func TestListNeverPresentsAnAboveThresholdCandidateAsAwaitingReview(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "ready", "s-1", "s-2", "s-3")

	got := f.list(t, ListParams{})

	if len(got.Pending) != 0 {
		t.Errorf("pending = %v, want an above-threshold candidate to be absent", ids(got.Pending))
	}
	if want := []string{"project/ready"}; !equalStrings(ids(got.DueForAutoPromotion), want) {
		t.Errorf("due = %v, want %v — it must not simply disappear", ids(got.DueForAutoPromotion), want)
	}
}

// TestListingWritesNothing is hard requirement 2 in one assertion: looking
// at the queue may not change it.
func TestListingWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	f.promote(t, memory.KindUser, "standing")

	before := treeDigest(t, f.base)
	for i := 0; i < 3; i++ {
		f.list(t, ListParams{})
		f.list(t, ListParams{Section: SectionPending})
		f.list(t, ListParams{Section: SectionPromoted})
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("listing changed the store: %s -> %s", before, after)
	}
}

func TestListSections(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	f.promote(t, memory.KindUser, "standing")

	pending := f.list(t, ListParams{Section: SectionPending})
	if len(pending.Promoted) != 0 || len(pending.Pending) != 1 {
		t.Errorf("pending section returned %+v", pending)
	}
	promoted := f.list(t, ListParams{Section: SectionPromoted})
	if len(promoted.Pending) != 0 || len(promoted.Promoted) != 1 {
		t.Errorf("promoted section returned %+v", promoted)
	}

	_, err := f.queue.List(context.Background(), ListParams{Section: "everything"})
	if !errors.Is(err, ErrUnknownSection) {
		t.Fatalf("unknown section error = %v, want ErrUnknownSection", err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v, want invalid_input", kind)
	}
}

func TestListOfAnEmptyQueueIsEmptyAndNotAnError(t *testing.T) {
	f := newFixture(t)
	got := f.list(t, ListParams{})
	if len(got.Pending) != 0 || len(got.Promoted) != 0 || len(got.Unreadable) != 0 {
		t.Errorf("empty store listed %+v", got)
	}
}

// TestUnreadableCandidateIsNamedNotDropped covers the honesty rule a
// listing shares with the record store: one damaged file must not make the
// candidates beside it invisible, and must not be silently omitted either.
func TestUnreadableCandidateIsNamedNotDropped(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "readable", "s-1")
	damaged := filepath.Join(f.base, "candidates", "project", "damaged.candidate.json")
	if err := os.WriteFile(damaged, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing a damaged candidate: %v", err)
	}

	got := f.list(t, ListParams{})

	if want := []string{"project/readable"}; !equalStrings(ids(got.Pending), want) {
		t.Errorf("pending = %v, want the readable candidate still listed", ids(got.Pending))
	}
	if len(got.Unreadable) != 1 || got.Unreadable[0].ID != "project/damaged" {
		t.Fatalf("unreadable = %+v, want the damaged candidate named", got.Unreadable)
	}
	if got.Unreadable[0].Reason == "" {
		t.Error("an unreadable row carried no reason")
	}
}

func TestListPropagatesACanceledContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.queue.List(ctx, ListParams{}); err == nil {
		t.Fatal("a canceled context still produced a listing")
	}
}

// equalStrings compares two address lists.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FuzzReviewRPCParams drives this package's external-input decoder — every
// byte it sees arrives from a peer over the daemon socket.
//
// The property is absolute: no input panics, and every refusal is a
// pkg/cascade taxonomy error rather than a bare one. A decode failure is a
// correct outcome, not a finding.
func FuzzReviewRPCParams(f *testing.F) {
	seedDir := filepath.Join("..", "..", "testdata", "fuzz", "FuzzReviewRPCParams")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		f.Add(data)
	}
	f.Add([]byte(nil))
	f.Add([]byte("null"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var listParams ListParams
		if err := decodeParams(MethodReviewList, json.RawMessage(data), &listParams); err != nil {
			assertTaxonomy(t, err)
		} else if err := validateSection(listParams.Section); err != nil {
			assertTaxonomy(t, err)
		}
		var actParams ActParams
		if err := decodeParams(MethodReviewAct, json.RawMessage(data), &actParams); err != nil {
			assertTaxonomy(t, err)
		}
	})
}

// assertTaxonomy fails unless err carries a pkg/cascade Kind.
func assertTaxonomy(t *testing.T, err error) {
	t.Helper()
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatalf("a refusal was not a taxonomy error: %v", err)
	}
}
