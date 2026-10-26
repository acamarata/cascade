package cmd

// Purpose: the flag path and the in-session /resume path are ONE path
//   (resumeIntoSession), and the resumed block reaches the next request as
//   its leading context exactly once, on both paths.
// SPORT: plugins/cascade-pa:cmd:resume (TEST) -- P1-E22-W5-S47-T2.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// tuiRecorder is a TUI-side client that records CreateThread tiers, the
// Resume request and every streamed request.
type tuiRecorder struct {
	mu          sync.Mutex
	createTiers []string
	resumeReq   ResumeRequest
	streamed    chan OneShotRequest
}

func (r *tuiRecorder) client() fakeClient {
	return fakeClient{
		CreateThreadFn: func(_ context.Context, tier string) (string, error) {
			r.mu.Lock()
			r.createTiers = append(r.createTiers, tier)
			r.mu.Unlock()
			return "thread-created", nil
		},
		ResumeFn: func(_ context.Context, req ResumeRequest) (ResumeResult, error) {
			r.mu.Lock()
			r.resumeReq = req
			r.mu.Unlock()
			return ResumeResult{ThreadID: req.TopicID, LeadingContext: "composed block"}, nil
		},
		StreamFn: func(_ context.Context, req OneShotRequest) (<-chan string, <-chan error) {
			r.streamed <- req
			tokens, errs := make(chan string), make(chan error, 1)
			close(tokens)
			errs <- nil
			close(errs)
			return tokens, errs
		},
	}
}

// tuiSubmit builds the real TUI submit closure over rec's client with a
// fresh, threadless session, and returns it with its sender.
func tuiSubmit(rec *tuiRecorder) (func(string) (func(), error), *recordingSender) {
	sender := &recordingSender{}
	var ref msgSender = sender
	submit := newSubmitFunc(context.Background(), "", rec.client(), &ref)
	return func(line string) (func(), error) {
		_, cancel := submit(line)
		return cancel, nil
	}, sender
}

// nextStreamed returns the next streamed request or fails after 2s.
func nextStreamed(t *testing.T, rec *tuiRecorder) OneShotRequest {
	t.Helper()
	select {
	case req := <-rec.streamed:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("no request was streamed within 2s")
		return OneShotRequest{}
	}
}

// TestResumeFlagsAndSlashCommandShareOnePath (contract name): --resume-topic
// and "/resume topic <id>" on a new session both create the session thread
// at the same tier and send the identical chat.resume request into it.
// Failing input: a /resume that resumes without a session thread.
func TestResumeFlagsAndSlashCommandShareOnePath(t *testing.T) {
	calls, err := runSession(t, "--resume-topic", "th1", "-m", "continue")
	if err != nil {
		t.Fatalf("flag path: %v", err)
	}
	rec := &tuiRecorder{streamed: make(chan OneShotRequest, 4)}
	submit, sender := tuiSubmit(rec)
	cancel, _ := submit("/resume topic th1")
	defer cancel()
	waitForMessages(t, sender, 1)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(calls.createTiers) != 1 || len(rec.createTiers) != 1 || calls.createTiers[0] != rec.createTiers[0] {
		t.Fatalf("create tiers flag=%q slash=%q, want one identical create each", calls.createTiers, rec.createTiers)
	}
	if calls.resumeReq != rec.resumeReq || rec.resumeReq.Thread != "thread-created" {
		t.Fatalf("flag request %+v != slash request %+v (want both into thread-created)", calls.resumeReq, rec.resumeReq)
	}
}

// TestResume_LeadingContext_PrependedToNextTurn (contract name): on both
// paths the next request carries the block as LeadingContext, on the
// session thread, and the request after it carries nothing. Failing input:
// printing the block without staging it, or re-sending it.
func TestResume_LeadingContext_PrependedToNextTurn(t *testing.T) {
	calls, err := runSession(t, "--resume-topic", "th1", "-m", "first")
	if err != nil {
		t.Fatalf("flag path: %v", err)
	}
	if got := calls.oneShot; len(got) != 1 || got[0].LeadingContext != "composed block" || got[0].Prompt != "first" {
		t.Fatalf("flag path next request = %+v, want the block ahead of the prompt", got)
	}

	rec := &tuiRecorder{streamed: make(chan OneShotRequest, 4)}
	submit, sender := tuiSubmit(rec)
	cancel1, _ := submit("/resume topic th1")
	defer cancel1()
	waitForMessages(t, sender, 1)
	cancel2, _ := submit("first")
	defer cancel2()
	next := nextStreamed(t, rec)
	if next.LeadingContext != "composed block" || next.Thread != "thread-created" || next.Privacy != "" {
		t.Fatalf("TUI next request = %+v, want the block on the session thread", next)
	}
	cancel3, _ := submit("second")
	defer cancel3()
	if after := nextStreamed(t, rec); after.LeadingContext != "" || after.Thread != "thread-created" {
		t.Fatalf("TUI later request = %+v, want no block (sent exactly once)", after)
	}
}

// TestResumeFlag_ErrorPropagates proves a Client.Resume error reaches
// Execute's own error and no turn is sent afterward.
func TestResumeFlag_ErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindPolicyDenied, "conversation: resume: thread is local-only")
	oneShotCalled := false
	SetClient(fakeClient{
		ResumeFn: func(context.Context, ResumeRequest) (ResumeResult, error) { return ResumeResult{}, wantErr },
		OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
			oneShotCalled = true
			return OneShotResult{}, nil
		},
	})
	t.Cleanup(func() { SetClient(nil) })

	c := NewChatCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetIn(&bytes.Buffer{})
	c.SetContext(context.Background())
	c.SetArgs([]string{"--resume-topic", "local-thread", "-m", "continue"})
	if err := c.Execute(); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("Execute err = %v, want %v", err, wantErr)
	}
	if oneShotCalled {
		t.Fatal("OneShot was called despite the resume refusal")
	}
}
