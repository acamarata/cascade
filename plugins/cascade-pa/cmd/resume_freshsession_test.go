package cmd

// Purpose: the session-thread rules of `cascade chat --resume-*`
//   (resume.go, resume_session.go) through the REAL cobra command: a new
//   session creates its thread with the flag tier (restricted by default)
//   and resumes INTO it; --thread X is never switched; a privacy flag is
//   never silently dropped.
// SPORT: plugins/cascade-pa:cmd:resume (TEST) -- P1-E22-W5-S47-T2.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// sessionCalls records every Client call one `cascade chat` run makes.
type sessionCalls struct {
	createTiers []string
	resumeReq   ResumeRequest
	oneShot     []OneShotRequest
}

// runSession executes the real command with a recording client whose
// CreateThread returns "thread-created".
func runSession(t *testing.T, argv ...string) (*sessionCalls, error) {
	t.Helper()
	calls := &sessionCalls{}
	SetClient(fakeClient{
		CreateThreadFn: func(_ context.Context, tier string) (string, error) {
			calls.createTiers = append(calls.createTiers, tier)
			return "thread-created", nil
		},
		ResumeFn: func(_ context.Context, req ResumeRequest) (ResumeResult, error) {
			calls.resumeReq = req
			return ResumeResult{ThreadID: req.TopicID, LeadingContext: "composed block"}, nil
		},
		OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
			calls.oneShot = append(calls.oneShot, req)
			return OneShotResult{ThreadID: req.Thread, TurnID: "t1", Content: "ok"}, nil
		},
	})
	t.Cleanup(func() { SetClient(nil) })
	c := NewChatCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetIn(&bytes.Buffer{})
	c.SetContext(context.Background())
	c.SetArgs(argv)
	return calls, c.Execute()
}

// assertResumedIntoCreated checks the new-session shape: one CreateThread
// at wantTier, the resume INTO that thread, and the one append on it with
// the block and no privacy_mode (the marker was written at creation).
func assertResumedIntoCreated(t *testing.T, calls *sessionCalls, wantTier string) {
	t.Helper()
	if len(calls.createTiers) != 1 || calls.createTiers[0] != wantTier {
		t.Fatalf("CreateThread tiers = %q, want exactly [%q]", calls.createTiers, wantTier)
	}
	if calls.resumeReq.Thread != "thread-created" {
		t.Fatalf("Resume session thread = %q, want the created thread", calls.resumeReq.Thread)
	}
	if len(calls.oneShot) != 1 {
		t.Fatalf("OneShot calls = %d, want 1", len(calls.oneShot))
	}
	got := calls.oneShot[0]
	if got.Thread != "thread-created" || got.Privacy != "" || got.LeadingContext != "composed block" {
		t.Fatalf("OneShot = %+v, want the created thread, no privacy_mode, the resumed block", got)
	}
}

// TestResumeNewSessionUsesFlagTier (contract name): --local-only on a new
// session creates the session thread local-only before the resume, and
// the prompt lands there -- never in the resumed (possibly public) topic.
// Failing input: dropping the flag, or appending to the topic thread.
func TestResumeNewSessionUsesFlagTier(t *testing.T) {
	calls, err := runSession(t, "--local-only", "--resume-topic", "PUBLIC-T", "-m", "secret")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertResumedIntoCreated(t, calls, chatPrivacyLocalOnly)
	if calls.resumeReq.TopicID != "PUBLIC-T" {
		t.Fatalf("Resume TopicID = %q, want PUBLIC-T", calls.resumeReq.TopicID)
	}
}

// TestResumeNewSessionNoFlagIsRestricted (contract name): with no privacy
// flag the session thread is created "restricted" -- explicitly, since
// chat.create_thread requires a tier. Failing input: sending "".
func TestResumeNewSessionNoFlagIsRestricted(t *testing.T) {
	calls, err := runSession(t, "--resume-query", "budget planning", "-m", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertResumedIntoCreated(t, calls, chatPrivacyRestricted)
}

// TestResumeNamedThreadNeverSwitched: with --thread X the resume is into X
// and the prompt goes to X; no thread is created.
func TestResumeNamedThreadNeverSwitched(t *testing.T) {
	calls, err := runSession(t, "--thread", "X", "--resume-topic", "T", "-m", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(calls.createTiers) != 0 || calls.resumeReq.Thread != "X" {
		t.Fatalf("creates = %q, resume into %q; want no create and resume into X", calls.createTiers, calls.resumeReq.Thread)
	}
	if len(calls.oneShot) != 1 || calls.oneShot[0].Thread != "X" || calls.oneShot[0].LeadingContext != "composed block" {
		t.Fatalf("OneShot = %+v, want one append to X carrying the block", calls.oneShot)
	}
}

// TestResumeNamedThreadWithFlagRefused: --local-only with --thread X and a
// resume is refused before any daemon call, never ignored.
func TestResumeNamedThreadWithFlagRefused(t *testing.T) {
	calls, err := runSession(t, "--local-only", "--thread", "X", "--resume-topic", "T", "-m", "hi")
	if !errors.Is(err, errChatPrivacyOnNamedThread) || !strings.Contains(err.Error(), errChatPrivacyOnNamedThread.Error()) {
		t.Fatalf("Execute err = %v, want errChatPrivacyOnNamedThread", err)
	}
	if len(calls.createTiers) != 0 || calls.resumeReq != (ResumeRequest{}) || len(calls.oneShot) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
}

// TestResumeRefusedCreatesNoTurn: a refused resume on a new session leaves
// the created thread without any append.
func TestResumeRefusedCreatesNoTurn(t *testing.T) {
	refusal := errors.New("policy denied: source more restrictive than the session")
	appended := false
	SetClient(fakeClient{
		ResumeFn: func(context.Context, ResumeRequest) (ResumeResult, error) { return ResumeResult{}, refusal },
		OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
			appended = true
			return OneShotResult{}, nil
		},
	})
	t.Cleanup(func() { SetClient(nil) })
	c := NewChatCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	c.SetArgs([]string{"--resume-topic", "LOCAL-T", "-m", "hi"})
	if err := c.Execute(); !errors.Is(err, refusal) || !strings.Contains(err.Error(), refusal.Error()) {
		t.Fatalf("Execute err = %v, want the refusal", err)
	}
	if appended {
		t.Fatal("a turn was appended after the resume was refused")
	}
}

// TestChatFreshSessionWithoutResumeCreatesNoThread: ordinary chat still
// lets the daemon mint and mark the thread on the first append; no
// chat.create_thread round trip happens.
func TestChatFreshSessionWithoutResumeCreatesNoThread(t *testing.T) {
	calls, err := runSession(t, "--local-only", "-m", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(calls.createTiers) != 0 || len(calls.oneShot) != 1 {
		t.Fatalf("creates = %q, oneShots = %d; want no create and one append", calls.createTiers, len(calls.oneShot))
	}
	if got := calls.oneShot[0]; got.Thread != "" || got.Privacy != chatPrivacyLocalOnly {
		t.Fatalf("OneShot = %+v, want Thread \"\" with privacy local-only", got)
	}
}

// TestResumeWithPrivacyFlagAppendsWithoutPrivacyMode (contract name): a
// privacy flag on a new resume session sets the tier ONCE, through
// chat.create_thread; every chat.append_turn the client then sends to that
// thread carries privacy_mode "" (the daemon refuses a mode on a named
// thread). Failing input: appendRequest keeping the flag tier after the
// thread exists, which the daemon would refuse as KindInvalidInput.
func TestResumeWithPrivacyFlagAppendsWithoutPrivacyMode(t *testing.T) {
	calls, err := runSession(t, "--local-only", "--resume-topic", "T1", "-m", "first")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(calls.createTiers) != 1 || calls.createTiers[0] != chatPrivacyLocalOnly {
		t.Fatalf("CreateThread tiers = %q, want exactly [%q]", calls.createTiers, chatPrivacyLocalOnly)
	}
	for i, req := range calls.oneShot {
		if req.Privacy != "" || req.Thread != "thread-created" {
			t.Errorf("append %d = %+v, want the created thread and no privacy_mode", i, req)
		}
	}

	// The session's own request builder: before a thread exists the flag
	// tier rides on the creating append; afterwards it never does.
	s := newChatSession("", chatPrivacyLocalOnly)
	if first := s.appendRequest("p", ""); first.Privacy != chatPrivacyLocalOnly || first.Thread != "" {
		t.Fatalf("pre-thread request = %+v, want the flag tier and no thread", first)
	}
	client := fakeClient{CreateThreadFn: func(context.Context, string) (string, error) { return "th-new", nil }}
	if _, err := s.ensure(context.Background(), client); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for i := 0; i < 2; i++ {
		if req := s.appendRequest("p", ""); req.Privacy != "" || req.Thread != "th-new" {
			t.Fatalf("post-thread request %d = %+v, want no privacy_mode on th-new", i, req)
		}
	}
}
