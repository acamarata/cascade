package conversation

// Purpose: unit tests for resume.go's budget-overflow graceful
//   degradation, idempotency, and trimReportLines -- split from
//   resume_test.go under Art.10.3's 300-line cap.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"strings"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResume_BudgetOverflow_GracefulDegradation: the failing input is a
// Composer result carrying a non-empty Trims slice -- Resume must NOT
// turn that into an error (the ticket's own "the session opens rather
// than hard-fails" acceptance criterion); it must surface Trimmed=true
// and a content-free TrimReport line instead.
func TestResume_BudgetOverflow_GracefulDegradation(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "kept")
	fc := &fakeComposer{result: cascadecontext.ComposedResult{
		Budget: 10, TokensUsed: 3,
		Slots: []cascadecontext.ComposedSlot{{Kind: cascadecontext.SlotKindHistory, Label: "turn:kept", Content: "kept", Tokens: 3}},
		Trims: []cascadecontext.BudgetTrimEvent{{Kind: cascadecontext.SlotKindHistory, Label: "turn:dropped", Reason: "budget exceeded", Dropped: true}},
	}}
	r, err := NewResumer(store, fc, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if err != nil {
		t.Fatalf("Resume must open the session (graceful degradation), got error: %v", err)
	}
	if !res.Trimmed {
		t.Fatalf("Trimmed = false, want true")
	}
	if len(res.TrimReport) != 1 || !strings.Contains(res.TrimReport[0], "turn:dropped") {
		t.Fatalf("TrimReport = %v, want one line naming turn:dropped", res.TrimReport)
	}
	for _, line := range res.TrimReport {
		if strings.Contains(line, "kept") {
			t.Fatalf("TrimReport line %q must never carry slot content", line)
		}
	}
}

// TestResume_BudgetOverflow_RealComposer proves graceful degradation holds
// against a REAL context.Composer -- provider.NaiveTokenCounter{}, the same construction
// production wiring uses (chat_resume_wiring.go), no summarizer.
// TestResume_BudgetOverflow_GracefulDegradation above only proves Resume
// forwards fakeComposer's own hand-written Trims field; it never proves a
// real Composer actually overflows and trims under a real budget. Five
// turns, each ~40 runes (~10 NaiveTokenCounter tokens, utf8-rune/4): a
// 12-token budget fits only the newest (buildHistorySlots' own
// newest-first priority order), forcing the real assembly loop to drop
// the rest.
func TestResume_BudgetOverflow_RealComposer(t *testing.T) {
	store := newResumeStore(t)
	ctx := context.Background()
	threadID, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	const turnContent = "this line is exactly forty characters!!"
	for i := int64(0); i < 5; i++ {
		turn := Turn{ID: NewTurnID(threadID, i, RoleUser), ThreadID: threadID, Seq: i, Role: RoleUser, CreatedAt: i + 1}
		if err := store.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
		seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0,
			Kind: SegmentText, Content: turnContent, CreatedAt: i + 1}
		if err := store.AppendSegment(ctx, seg); err != nil {
			t.Fatalf("AppendSegment %d: %v", i, err)
		}
	}
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	r, err := NewResumer(store, composer, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	const budget = 12
	res, err := r.Resume(ctx, ResumeRequest{TopicID: threadID, Budget: budget})
	if err != nil {
		t.Fatalf("Resume over budget must open the session (graceful degradation), got error: %v", err)
	}
	if !res.Trimmed {
		t.Fatalf("Trimmed = false, want true (5 turns over a %d-token budget)", budget)
	}
	if len(res.TrimReport) == 0 {
		t.Fatal("TrimReport is empty, want at least one line")
	}
	for _, line := range res.TrimReport {
		if strings.Contains(line, turnContent) {
			t.Fatalf("TrimReport line %q carries slot content, want content-free", line)
		}
	}
	if res.TokensUsed > budget {
		t.Fatalf("TokensUsed = %d, want <= budget %d", res.TokensUsed, budget)
	}
}

// TestResume_Idempotent: two Resume calls with the same request and
// unchanged underlying store data return the same composed result --
// Resume performs no write, so repeat calls are pure reads.
func TestResume_Idempotent(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "stable content")
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	req := ResumeRequest{TopicID: threadID}
	first, err := r.Resume(context.Background(), req)
	if err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	second, err := r.Resume(context.Background(), req)
	if err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if first.Context != second.Context || first.TokensUsed != second.TokensUsed {
		t.Fatalf("Resume is not idempotent: first=%+v second=%+v", first, second)
	}
	turns, err := store.ListTurns(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("Resume must never write: thread has %d turns after two Resume calls, want 1", len(turns))
	}
}

// TestTrimReportLines_TrimmedIncludesKeptTokens covers trimReportLines'
// second branch directly (a trimmed-not-dropped event).
func TestTrimReportLines_TrimmedIncludesKeptTokens(t *testing.T) {
	lines := trimReportLines([]cascadecontext.BudgetTrimEvent{
		{Label: "turn:x", Reason: "summarized", Dropped: false, KeptTokens: 7},
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "kept 7 tokens") {
		t.Fatalf("trimReportLines = %v, want a line reporting kept 7 tokens", lines)
	}
}
