package conversation

// Purpose: thread-seeding helpers for the resume tests. Split from
//   resume_test.go under Art.10.3's 300-line cap.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// seedResumeThread appends one user turn with content to a freshly minted
// thread in store, and returns the thread id.
func seedResumeThread(t *testing.T, store Store, content string) string {
	t.Helper()
	return seedResumeThreadAt(t, store, provider.SensitivityRestricted, content)
}

// seedResumeThreadAt is seedResumeThread with the thread created at tier:
// the marker is written with the thread row, before the first turn, as
// chat.create_thread does (a marker cannot be added to a thread that
// already holds turns).
func seedResumeThreadAt(t *testing.T, store Store, tier provider.SensitivityTier, content string) string {
	t.Helper()
	ctx := context.Background()
	threadID, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	seedOwnedThread(t, store, threadID, tier)
	turn := Turn{ID: NewTurnID(threadID, 0, RoleUser), ThreadID: threadID, Seq: 0, Role: RoleUser, CreatedAt: 1}
	if err := store.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: content, CreatedAt: 1}
	if err := store.AppendSegment(ctx, seg); err != nil {
		t.Fatalf("AppendSegment: %v", err)
	}
	return threadID
}

// seedLegacyUnmarkedThread writes a thread the way every thread predating
// privacy markers was written: turns and no marker row, which reads as
// restricted. Nothing creates such a thread any more; stored ones remain.
func seedLegacyUnmarkedThread(t *testing.T, store Store, content string) string {
	t.Helper()
	ctx := context.Background()
	threadID, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	turn := Turn{ID: NewTurnID(threadID, 0, RoleUser), ThreadID: threadID, Seq: 0, Role: RoleUser, CreatedAt: 1}
	if err := store.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: content, CreatedAt: 1}
	if err := store.AppendSegment(ctx, seg); err != nil {
		t.Fatalf("AppendSegment: %v", err)
	}
	return threadID
}
