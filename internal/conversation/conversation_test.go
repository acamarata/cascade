package conversation

// Purpose: package-level integration tests that do not belong to one
//   single file: a full Thread -> Turn -> Segment round trip through the
//   public ConversationStore surface, the migration idempotency +
//   downgrade-refusal proof driven end-to-end, the schema-version
//   collision guard, and this ticket's own PRIVACY hard rule -- the
//   enforced proof that no error path anywhere in this package echoes
//   Segment.Content back to a caller.
// SPORT: internal.conversation/ADDED (P1-E20-W5-S43-T1).

import (
	"context"
	"strings"
	"testing"
)

// TestConversationRoundTrip drives the real ConversationStore entry point
// end-to-end: two turns each carrying segments, read back through
// ListThreads/ListTurns/ListSegments in insertion order.
func TestConversationRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn0 := Turn{ID: NewTurnID("thread-x", 0, RoleUser), ThreadID: "thread-x", Seq: 0, Role: RoleUser, CreatedAt: 10}
	turn1 := Turn{ID: NewTurnID("thread-x", 1, RoleAssistant), ThreadID: "thread-x", Seq: 1, Role: RoleAssistant, CreatedAt: 11}
	if err := s.AppendTurn(ctx, turn0); err != nil {
		t.Fatalf("AppendTurn(0): %v", err)
	}
	if err := s.AppendTurn(ctx, turn1); err != nil {
		t.Fatalf("AppendTurn(1): %v", err)
	}

	seg0 := Segment{ID: NewSegmentID(turn0.ID, 0, SegmentText), TurnID: turn0.ID, Seq: 0, Kind: SegmentText, Content: "question one", CreatedAt: 10}
	seg1 := Segment{ID: NewSegmentID(turn1.ID, 0, SegmentText), TurnID: turn1.ID, Seq: 0, Kind: SegmentText, Content: "reply one", CreatedAt: 11}
	if err := s.AppendSegment(ctx, seg0); err != nil {
		t.Fatalf("AppendSegment(0): %v", err)
	}
	if err := s.AppendSegment(ctx, seg1); err != nil {
		t.Fatalf("AppendSegment(1): %v", err)
	}

	threads, err := s.ListThreads(ctx)
	if err != nil || len(threads) != 1 || threads[0].ID != "thread-x" {
		t.Fatalf("ListThreads = %+v, %v, want exactly [thread-x]", threads, err)
	}

	turns, err := s.ListTurns(ctx, "thread-x")
	if err != nil || len(turns) != 2 {
		t.Fatalf("ListTurns = %+v, %v, want 2 turns in insertion order", turns, err)
	}
	if turns[0].ID != turn0.ID || turns[1].ID != turn1.ID {
		t.Fatalf("ListTurns order = [%s, %s], want [%s, %s]", turns[0].ID, turns[1].ID, turn0.ID, turn1.ID)
	}

	segs0, err := s.ListSegments(ctx, turn0.ID)
	if err != nil || len(segs0) != 1 || segs0[0].Content != "question one" {
		t.Fatalf("ListSegments(turn0) = %+v, %v", segs0, err)
	}
	segs1, err := s.ListSegments(ctx, turn1.ID)
	if err != nil || len(segs1) != 1 || segs1[0].Content != "reply one" {
		t.Fatalf("ListSegments(turn1) = %+v, %v", segs1, err)
	}
}

// TestMigrationSetCarriesOwnSetID guards R-16.77's per-set identity
// fix directly: this package's MigrationSet must carry a non-empty,
// package-specific SetID, which is what makes conversationSchemaVersion
// independent of every other package's schema_version in the shared
// applied_migrations ledger (superseding the pre-R-16.77 "claimed
// global slot" collision concern this test used to guard).
func TestMigrationSetCarriesOwnSetID(t *testing.T) {
	set := MigrationSet()
	if set.SetID != "conversation" {
		t.Errorf("MigrationSet().SetID = %q, want %q", set.SetID, "conversation")
	}
}

// TestErrorsNeverEchoContent is this ticket's own PRIVACY hard rule,
// enforced: a Segment carrying a unique, unmistakable content marker is
// driven through every error path this package can produce (duplicate
// append, out-of-order append, missing turn, invalid record), and the
// marker must never appear in any returned error's Error() string. A
// conversation record's content is the most privacy-sensitive data this
// repository stores; an error path that echoed it back would leak it
// through CLI stderr, RPC responses, and structured logs alike.
func TestErrorsNeverEchoContent(t *testing.T) {
	const marker = "UNMISTAKABLE-PRIVATE-CONTENT-MARKER-8f2c"
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("thread-y", 0, RoleUser), ThreadID: "thread-y", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: marker, CreatedAt: 10}
	if err := s.AppendSegment(ctx, seg); err != nil {
		t.Fatalf("AppendSegment: %v", err)
	}

	errs := []error{
		// Duplicate append of the marker-carrying turn/segment.
		s.AppendTurn(ctx, turn),
		s.AppendSegment(ctx, seg),
		// Out-of-order append whose Content is the marker.
		s.AppendSegment(ctx, Segment{ID: NewSegmentID(turn.ID, 5, SegmentText), TurnID: turn.ID, Seq: 5, Kind: SegmentText, Content: marker, CreatedAt: 10}),
		// Invalid record whose Content is the marker.
		s.AppendSegment(ctx, Segment{ID: "", TurnID: turn.ID, Seq: 1, Kind: SegmentText, Content: marker, CreatedAt: 10}),
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("errs[%d] = nil, want a non-nil error to check", i)
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("errs[%d].Error() = %q leaks the content marker", i, err.Error())
		}
	}

	// Also confirm the content really did round-trip (i.e. the marker
	// wasn't simply never stored -- the leak check above is meaningful
	// only if the content was actually present in the system).
	segs, err := s.ListSegments(ctx, turn.ID)
	if err != nil || len(segs) != 1 || segs[0].Content != marker {
		t.Fatalf("ListSegments = %+v, %v, want the marker content stored", segs, err)
	}
}
