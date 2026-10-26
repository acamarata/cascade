package cmd

// Purpose: unit tests for the V/S-47.T2 "/resume" in-session command --
//   newSubmitFunc's parseResumeCommand branch and pumpResume
//   (chat_tui_run.go), and Model's resumeDoneMsg/resumeErrMsg handling
//   (chat_tui.go). Mirrors chat_tui_run_test.go's established
//   recordingSender pattern: no tea.Program, no TTY.
// SPORT: plugins/cascade-pa:cmd:chat (TEST) -- P1-E22-W5-S47-T2.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitForMessages polls rec until it holds at least n messages or the
// 2s deadline passes -- the same bounded-poll shape
// TestNewSubmitFuncDeliversTokensAndDone already establishes in this
// package for the identical async-delivery problem.
func waitForMessages(t *testing.T, rec *recordingSender, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		got := len(rec.msgs)
		rec.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recordingSender did not reach %d messages within 2s", n)
}

// TestNewSubmitFunc_ResumeCommand_DoesNotStream proves a "/resume ..."
// line is routed to pumpResume, NOT pumpStream: the failing input this
// guards is a StreamFn that panics if called (fakeClient.Stream panics
// on a nil StreamFn), so if newSubmitFunc ever fell through to the
// ordinary chat path for a /resume line, this test would panic instead
// of passing.
func TestNewSubmitFunc_ResumeCommand_DoesNotStream(t *testing.T) {
	client := fakeClient{ResumeFn: func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{LeadingContext: "resumed block"}, nil
	}}
	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "th1", client, &sender)
	cmd, cancel := submit("/resume topic abc123")
	if cmd != nil {
		t.Fatal("cmd must be nil (delivery is via msgSender.Send)")
	}
	defer cancel()

	waitForMessages(t, rec, 1)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	got, ok := rec.msgs[0].(resumeDoneMsg)
	if !ok {
		t.Fatalf("message = %T, want resumeDoneMsg", rec.msgs[0])
	}
	if got.context != "resumed block" {
		t.Fatalf("resumeDoneMsg.context = %q, want %q", got.context, "resumed block")
	}
}

// TestNewSubmitFunc_ResumeCommand_ParsesQueryForm proves the "/resume
// query <terms>" form reaches Client.Resume with Query set, TopicID
// empty.
func TestNewSubmitFunc_ResumeCommand_ParsesQueryForm(t *testing.T) {
	var got ResumeRequest
	client := fakeClient{ResumeFn: func(_ context.Context, req ResumeRequest) (ResumeResult, error) {
		got = req
		return ResumeResult{LeadingContext: "ok"}, nil
	}}
	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "", client, &sender)
	_, cancel := submit("/resume query find my keys")
	defer cancel()
	waitForMessages(t, rec, 1)
	if got.Query != "find my keys" || got.TopicID != "" {
		t.Fatalf("Resume called with %+v, want Query set and TopicID empty", got)
	}
}

// TestNewSubmitFunc_ResumeCommand_CarriesSessionThread proves the
// in-session "/resume" resumes INTO the session's own thread, the same
// destination the flag path uses. Failing input: the session thread never
// reaching chat.resume's session_thread_id.
func TestNewSubmitFunc_ResumeCommand_CarriesSessionThread(t *testing.T) {
	var got ResumeRequest
	client := fakeClient{ResumeFn: func(_ context.Context, req ResumeRequest) (ResumeResult, error) {
		got = req
		return ResumeResult{LeadingContext: "ok"}, nil
	}}
	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "the-session-thread", client, &sender)
	_, cancel := submit("/resume topic abc123")
	defer cancel()
	waitForMessages(t, rec, 1)
	if got.Thread != "the-session-thread" {
		t.Fatalf("Resume called with Thread=%q, want %q (the session's own current thread)", got.Thread, "the-session-thread")
	}
}

// TestPumpResume_ErrorDeliversResumeErrMsg proves a Resume failure
// delivers resumeErrMsg, not a panic or a silently dropped message.
func TestPumpResume_ErrorDeliversResumeErrMsg(t *testing.T) {
	wantErr := errors.New("thread is local-only")
	client := fakeClient{ResumeFn: func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{}, wantErr
	}}
	rec := &recordingSender{}
	pumpResume(context.Background(), rec, client, newChatSession("session-1", ""), ResumeRequest{TopicID: "t1"}, &pendingLeadingContext{})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) != 1 {
		t.Fatalf("recorded %d messages, want exactly 1", len(rec.msgs))
	}
	got, ok := rec.msgs[0].(resumeErrMsg)
	if !ok {
		t.Fatalf("message = %T, want resumeErrMsg", rec.msgs[0])
	}
	if !errors.Is(got.err, wantErr) {
		t.Fatalf("resumeErrMsg.err = %v, want %v", got.err, wantErr)
	}
}

// TestPumpResume_CanceledContext_SendsNothing: the failing input is a
// context already canceled before Resume returns -- pumpResume must not
// send on a Program that may already be torn down, matching pumpStream's
// own ctx.Done() short-circuit.
func TestPumpResume_CanceledContext_SendsNothing(t *testing.T) {
	block := make(chan struct{})
	client := fakeClient{ResumeFn: func(ctx context.Context, _ ResumeRequest) (ResumeResult, error) {
		<-block
		return ResumeResult{}, ctx.Err()
	}}
	rec := &recordingSender{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pumpResume(ctx, rec, client, newChatSession("session-1", ""), ResumeRequest{TopicID: "t1"}, &pendingLeadingContext{})
		close(done)
	}()
	cancel()
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpResume did not return within 2s of cancellation")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) != 0 {
		t.Fatalf("recorded %d messages after cancellation, want 0", len(rec.msgs))
	}
}

// TestNewSubmitFunc_ResumeThenPrompt_CarriesLeadingContext proves the
// ordinary prompt submitted right after a completed "/resume" carries the
// resumed block as
// OneShotRequest.LeadingContext -- asserted on the real request
// newSubmitFunc's own pumpStream call builds, not merely on what the
// Model renders. The failing input this guards is a submit closure that
// never threads pendingLeadingContext through to the Stream call at all.
func TestNewSubmitFunc_ResumeThenPrompt_CarriesLeadingContext(t *testing.T) {
	var gotStream OneShotRequest
	streamCalled := make(chan struct{}, 1)
	client := fakeClient{
		ResumeFn: func(context.Context, ResumeRequest) (ResumeResult, error) {
			return ResumeResult{LeadingContext: "resumed block"}, nil
		},
		StreamFn: func(_ context.Context, req OneShotRequest) (<-chan string, <-chan error) {
			gotStream = req
			tokens := make(chan string)
			errs := make(chan error, 1)
			close(tokens)
			errs <- nil
			close(errs)
			streamCalled <- struct{}{}
			return tokens, errs
		},
	}
	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "th1", client, &sender)

	_, cancel1 := submit("/resume topic abc123")
	defer cancel1()
	waitForMessages(t, rec, 1) // resumeDoneMsg -- pending is set before this send

	_, cancel2 := submit("continue please")
	defer cancel2()
	select {
	case <-streamCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream was never called for the prompt after /resume")
	}
	if gotStream.LeadingContext != "resumed block" {
		t.Fatalf("OneShotRequest.LeadingContext = %q, want %q (the completed resume's own block)", gotStream.LeadingContext, "resumed block")
	}
	if gotStream.Prompt != "continue please" {
		t.Fatalf("OneShotRequest.Prompt = %q, want %q", gotStream.Prompt, "continue please")
	}
}

// TestModel_ResumeDoneMsg_RendersContextAndWarnings drives Update
// directly (no tea.Program) and proves resumeDoneMsg renders the context
// block plus one warning line per TrimReport entry, and clears the
// streaming/busy flag so the next Enter is not blocked.
func TestModel_ResumeDoneMsg_RendersContextAndWarnings(t *testing.T) {
	m := Model{streaming: true}
	next, cmd := m.Update(resumeDoneMsg{context: "resumed text", trimReport: []string{"turn:x: dropped"}})
	nm := next.(Model)
	if cmd != nil {
		t.Fatal("Update(resumeDoneMsg) cmd must be nil")
	}
	if nm.streaming {
		t.Fatal("Update(resumeDoneMsg) must clear streaming")
	}
	if len(nm.lines) != 2 {
		t.Fatalf("lines = %v, want 2 (context + one warning)", nm.lines)
	}
}

// TestModel_ResumeErrMsg_RendersErrorLine mirrors streamErrMsg's own
// established Update behavior for the resume-specific message type.
func TestModel_ResumeErrMsg_RendersErrorLine(t *testing.T) {
	m := Model{streaming: true}
	next, _ := m.Update(resumeErrMsg{err: errors.New("boom")})
	nm := next.(Model)
	if nm.streaming {
		t.Fatal("Update(resumeErrMsg) must clear streaming")
	}
	if len(nm.lines) != 1 {
		t.Fatalf("lines = %v, want 1", nm.lines)
	}
}
