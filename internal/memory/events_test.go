package memory

// Purpose: the digest payload contract under test — its bus name, the
//   projection from a candidate view, and the property the whole event
//   rests on: the payload carries addresses and counts, never a record's
//   text. The shape is asserted on the MARSHALLED bytes, since those are
//   what a subscriber actually receives.
// SPORT: event:MemoryWeeklyDigest (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSummarizeCandidateCarriesEvidenceNotContent(t *testing.T) {
	promoted := fixedNow.Add(-time.Hour)
	snooze := fixedNow.Add(time.Hour)
	got := SummarizeCandidate(CandidateEntry{
		Name: "a-record", Kind: KindProject,
		SessionIDs: []string{"s-1", "s-2"}, RefCount: 2,
		PromotedAt: &promoted, SnoozeUntil: &snooze, Status: CandidatePending,
	})

	if got.ID != "project/a-record" {
		t.Errorf("ID = %q, want the canonical address", got.ID)
	}
	if got.Sessions != 2 || got.RefCount != 2 {
		t.Errorf("summary = %+v, want the counted evidence", got)
	}
	if got.PromotedAt == nil || !got.PromotedAt.Equal(promoted) ||
		got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(snooze) {
		t.Errorf("timestamps = %v / %v, want both carried", got.PromotedAt, got.SnoozeUntil)
	}
	// The summary's pointers are copies: a caller cannot reach ledger
	// state through what it received.
	*got.PromotedAt = fixedNow
	if promoted.Equal(fixedNow) {
		t.Error("editing the summary reached back into the caller's own value")
	}
}

// TestDigestPayloadNamesNoContentField is a structural guard, not a
// behavioural one: it fails the moment a field is added to the payload
// whose name suggests record text, which is the only way the
// "addresses, never content" rule could be broken from inside this
// package.
func TestDigestPayloadNamesNoContentField(t *testing.T) {
	now := fixedNow
	digest := MemoryWeeklyDigest{
		Since: now.Add(-7 * 24 * time.Hour), Until: now,
		MinRefCount: PromotionMinRefCount, MinSessions: PromotionMinSessions,
		Pending: []CandidateSummary{SummarizeCandidate(CandidateEntry{
			Name: "a-record", Kind: KindProject, RefCount: 1,
			SessionIDs: []string{"s-1"}, Status: CandidatePending,
		})},
		Unreadable: []string{"user/damaged"},
	}
	data, err := json.Marshal(digest)
	if err != nil {
		t.Fatalf("marshalling the digest: %v", err)
	}
	rendered := string(data)
	for _, forbidden := range []string{"body", "description", "draft", "session_ids"} {
		if strings.Contains(rendered, `"`+forbidden+`"`) {
			t.Errorf("the digest payload carries a %q member:\n%s", forbidden, rendered)
		}
	}
	for _, required := range []string{"since", "until", "min_ref_count", "min_sessions", "pending"} {
		if !strings.Contains(rendered, `"`+required+`"`) {
			t.Errorf("the digest payload omits %q, which a reader needs to check "+
				"its claims:\n%s", required, rendered)
		}
	}
	if digest.EventName() != MemoryWeeklyDigestEvent {
		t.Errorf("event name = %q, want %q", digest.EventName(), MemoryWeeklyDigestEvent)
	}
}

func TestDiscardDigestEventsIsAWorkingSink(t *testing.T) {
	if err := DiscardDigestEvents().MemoryWeeklyDigestReady(
		context.Background(), MemoryWeeklyDigest{}); err != nil {
		t.Errorf("the discarding sink refused an event: %v", err)
	}
}
