package conversation

// Purpose: store.go's own tests -- append/read round trips, the
//   append-only invariant (ErrImmutable on a duplicate id, exercised
//   explicitly per this ticket's own acceptance criteria), out-of-order
//   refusal, missing-turn refusal, insertion-order listing, and clock
//   injection (CreatedAt always reflects the caller-supplied value, never
//   a value this package invented from the wall clock).
// SPORT: internal.conversation.store/ADDED (P1-E20-W5-S43-T1).

import (
	"context"
	"errors"
	"testing"
)

func TestAppendTurnCreatesThreadAndTurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	thread, ok, err := s.GetThread(ctx, "t1")
	if err != nil || !ok {
		t.Fatalf("GetThread(t1) = %+v, %v, %v, want a thread", thread, ok, err)
	}
	if thread.ID != "t1" {
		t.Errorf("thread.ID = %q, want t1", thread.ID)
	}

	turns, err := s.ListTurns(ctx, "t1")
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != turn.ID || turns[0].CreatedAt != 100 {
		t.Errorf("ListTurns = %+v, want [%+v]", turns, turn)
	}
}

// TestAppendTurnDuplicateIDReturnsErrImmutable exercises the append-only
// error path explicitly, per this ticket's acceptance criteria: appending
// the exact same turn twice must be refused, never silently accepted as a
// second write to the same row.
func TestAppendTurnDuplicateIDReturnsErrImmutable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("first AppendTurn: %v", err)
	}
	err := s.AppendTurn(ctx, turn)
	if !errors.Is(err, ErrImmutable) {
		t.Fatalf("second AppendTurn(same id) = %v, want ErrImmutable", err)
	}

	// The row itself must be untouched by the refused second attempt.
	turns, lerr := s.ListTurns(ctx, "t1")
	if lerr != nil || len(turns) != 1 {
		t.Fatalf("ListTurns after refused duplicate = %+v, %v, want exactly one turn", turns, lerr)
	}
}

func TestAppendTurnOutOfOrderRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seq=1 with no Seq=0 yet is out of order.
	turn := Turn{ID: NewTurnID("t1", 1, RoleUser), ThreadID: "t1", Seq: 1, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, turn); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("AppendTurn(seq=1 first) = %v, want ErrOutOfOrder", err)
	}

	// A correct Seq=0 followed by a repeated Seq=0 is also out of order
	// (distinct from the same-id duplicate case above: different id, same
	// position).
	first := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, first); err != nil {
		t.Fatalf("AppendTurn(seq=0): %v", err)
	}
	repeat := Turn{ID: NewTurnID("t1", 0, RoleAssistant), ThreadID: "t1", Seq: 0, Role: RoleAssistant, CreatedAt: 101}
	if err := s.AppendTurn(ctx, repeat); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("AppendTurn(repeated seq=0, different id) = %v, want ErrOutOfOrder", err)
	}
}

func TestAppendSegmentRequiresExistingTurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seg := Segment{ID: NewSegmentID("missing-turn", 0, SegmentText), TurnID: "missing-turn", Seq: 0, Kind: SegmentText, Content: "hello", CreatedAt: 100}
	if err := s.AppendSegment(ctx, seg); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("AppendSegment(missing turn) = %v, want ErrTurnNotFound", err)
	}
}

func TestAppendSegmentDuplicateIDReturnsErrImmutable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: "hello", CreatedAt: 100}
	if err := s.AppendSegment(ctx, seg); err != nil {
		t.Fatalf("first AppendSegment: %v", err)
	}
	if err := s.AppendSegment(ctx, seg); !errors.Is(err, ErrImmutable) {
		t.Fatalf("second AppendSegment(same id) = %v, want ErrImmutable", err)
	}
}

func TestAppendSegmentOutOfOrderRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 100}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 1, SegmentText), TurnID: turn.ID, Seq: 1, Kind: SegmentText, Content: "hello", CreatedAt: 100}
	if err := s.AppendSegment(ctx, seg); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("AppendSegment(seq=1 first) = %v, want ErrOutOfOrder", err)
	}
}

func TestListTurnsAndSegmentsInsertionOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	roles := []Role{RoleUser, RoleAssistant, RoleUser}
	for i, r := range roles {
		turn := Turn{ID: NewTurnID("t1", int64(i), r), ThreadID: "t1", Seq: int64(i), Role: r, CreatedAt: int64(100 + i)}
		if err := s.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("AppendTurn(%d): %v", i, err)
		}
	}
	turns, err := s.ListTurns(ctx, "t1")
	if err != nil || len(turns) != 3 {
		t.Fatalf("ListTurns = %+v, %v, want 3 turns", turns, err)
	}
	for i, turn := range turns {
		if turn.Seq != int64(i) || turn.Role != roles[i] {
			t.Errorf("turns[%d] = %+v, want seq=%d role=%s", i, turn, i, roles[i])
		}
	}

	kinds := []SegmentKind{SegmentText, SegmentCode}
	for i, k := range kinds {
		seg := Segment{ID: NewSegmentID(turns[0].ID, int64(i), k), TurnID: turns[0].ID, Seq: int64(i), Kind: k, Content: "c", CreatedAt: int64(200 + i)}
		if err := s.AppendSegment(ctx, seg); err != nil {
			t.Fatalf("AppendSegment(%d): %v", i, err)
		}
	}
	segs, err := s.ListSegments(ctx, turns[0].ID)
	if err != nil || len(segs) != 2 {
		t.Fatalf("ListSegments = %+v, %v, want 2 segments", segs, err)
	}
	for i, seg := range segs {
		if seg.Seq != int64(i) || seg.Kind != kinds[i] {
			t.Errorf("segs[%d] = %+v, want seq=%d kind=%s", i, seg, i, kinds[i])
		}
	}
}

func TestListThreadsOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i, id := range []string{"a", "b"} {
		turn := Turn{ID: NewTurnID(id, 0, RoleUser), ThreadID: id, Seq: 0, Role: RoleUser, CreatedAt: int64(100 + i)}
		if err := s.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("AppendTurn(%s): %v", id, err)
		}
	}
	threads, err := s.ListThreads(ctx)
	if err != nil || len(threads) != 2 {
		t.Fatalf("ListThreads = %+v, %v, want 2 threads", threads, err)
	}
	if threads[0].ID != "a" || threads[1].ID != "b" {
		t.Errorf("ListThreads order = [%s, %s], want [a, b]", threads[0].ID, threads[1].ID)
	}
}

func TestGetThreadNotFound(t *testing.T) {
	s := newTestStore(t)
	thread, ok, err := s.GetThread(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("GetThread(missing) = %+v, %v, %v, want ok=false nil error", thread, ok, err)
	}
}

// TestClockInjectionNeverOverridesCallerTimestamp proves the store never
// substitutes a wall-clock value for the caller-supplied CreatedAt --
// every timestamp round-trips exactly, which is only possible if this
// package injects its clock rather than reading time.Now().
func TestClockInjectionNeverOverridesCallerTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const distinctiveTimestamp int64 = 1234567890

	turn := Turn{ID: NewTurnID("t1", 0, RoleUser), ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: distinctiveTimestamp}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	turns, err := s.ListTurns(ctx, "t1")
	if err != nil || len(turns) != 1 || turns[0].CreatedAt != distinctiveTimestamp {
		t.Fatalf("ListTurns = %+v, %v, want CreatedAt=%d preserved verbatim", turns, err, distinctiveTimestamp)
	}
}

func TestInvalidRecordsRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []Turn{
		{ID: "", ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 1},
		{ID: "x", ThreadID: "", Seq: 0, Role: RoleUser, CreatedAt: 1},
		{ID: "x", ThreadID: "t1", Seq: -1, Role: RoleUser, CreatedAt: 1},
		{ID: "x", ThreadID: "t1", Seq: 0, Role: RoleUser, CreatedAt: 0},
		{ID: "x", ThreadID: "t1", Seq: 0, Role: Role("bogus"), CreatedAt: 1},
	}
	for i, tc := range cases {
		if err := s.AppendTurn(ctx, tc); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("case %d: AppendTurn(%+v) = %v, want ErrInvalidRecord", i, tc, err)
		}
	}

	segCases := []Segment{
		{ID: "", TurnID: "t1", Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 1},
		{ID: "s", TurnID: "", Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 1},
		{ID: "s", TurnID: "t1", Seq: -1, Kind: SegmentText, Content: "x", CreatedAt: 1},
		{ID: "s", TurnID: "t1", Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 0},
		{ID: "s", TurnID: "t1", Seq: 0, Kind: SegmentKind("bogus"), Content: "x", CreatedAt: 1},
	}
	for i, tc := range segCases {
		if err := s.AppendSegment(ctx, tc); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("seg case %d: AppendSegment(%+v) = %v, want ErrInvalidRecord", i, tc, err)
		}
	}
}
