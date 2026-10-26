package cmd

// Purpose: unit and real-cobra-command tests for resume.go -- flag
//   validation, --resume-topic/--resume-query reaching the real
//   Client.Resume call (mirroring chat_privacy_test.go's runChatCommand
//   pattern), result rendering, and the `/resume` command parser.
// SPORT: plugins/cascade-pa:cmd:resume (TEST) -- P1-E22-W5-S47-T2.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestValidateResumeFlags mirrors chat_privacy_test.go's own shape for
// the identical mutual-exclusivity pattern.
func TestValidateResumeFlags(t *testing.T) {
	if err := validateResumeFlags(chatOptions{}); err != nil {
		t.Fatalf("neither flag set: got %v, want nil", err)
	}
	if err := validateResumeFlags(chatOptions{resumeTopic: "t1"}); err != nil {
		t.Fatalf("topic only: got %v, want nil", err)
	}
	if err := validateResumeFlags(chatOptions{resumeQuery: "q1"}); err != nil {
		t.Fatalf("query only: got %v, want nil", err)
	}
	err := validateResumeFlags(chatOptions{resumeTopic: "t1", resumeQuery: "q1"})
	if !errors.Is(err, errResumeFlagsConflict) || !strings.Contains(err.Error(), errResumeFlagsConflict.Error()) {
		t.Fatalf("both flags: got %v, want errResumeFlagsConflict", err)
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("both flags: got %v, want KindInvalidInput", err)
	}
}

// TestResumeCwd_NonEmpty proves resumeCwd resolves to a real, non-empty
// directory in a normal test environment: the session signal the query
// path forwards to recall.what's scope resolution.
func TestResumeCwd_NonEmpty(t *testing.T) {
	if resumeCwd() == "" {
		t.Fatal("resumeCwd() = \"\", want a real working directory in a normal test environment")
	}
}

// TestParseResumeCommand covers both recognized forms and the failing
// inputs that must NOT be recognized (an ordinary prompt, and a
// "/resume" line missing its keyword or argument).
func TestParseResumeCommand(t *testing.T) {
	cases := []struct {
		input   string
		wantOK  bool
		wantReq ResumeRequest
	}{
		{"/resume topic abc123", true, ResumeRequest{TopicID: "abc123"}},
		{"/resume query where did I put the keys", true, ResumeRequest{Query: "where did I put the keys"}},
		{"hello, how are you?", false, ResumeRequest{}},
		{"/resume", false, ResumeRequest{}},
		{"/resume topic", false, ResumeRequest{}},
		{"/resume bogus abc123", false, ResumeRequest{}},
	}
	for _, tc := range cases {
		req, ok := parseResumeCommand(tc.input)
		if ok != tc.wantOK {
			t.Errorf("parseResumeCommand(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			continue
		}
		if ok && req != tc.wantReq {
			t.Errorf("parseResumeCommand(%q) = %+v, want %+v", tc.input, req, tc.wantReq)
		}
	}
}

// runResumeCommand executes the REAL `cascade chat` cobra command with
// argv, capturing the ResumeRequest the client was handed plus stdout/
// stderr -- mirroring chat_privacy_test.go's runChatCommand: driving
// cobra rather than calling runResumeAndPrint directly is the point, a
// flag resolved correctly but never registered would pass a narrower
// test and fail for every operator.
func runResumeCommand(t *testing.T, resumeFn func(context.Context, ResumeRequest) (ResumeResult, error), argv ...string) (ResumeRequest, string, string, error) {
	t.Helper()
	var got ResumeRequest
	SetClient(fakeClient{
		ResumeFn: func(ctx context.Context, req ResumeRequest) (ResumeResult, error) {
			got = req
			return resumeFn(ctx, req)
		},
		// Every case in this file passes -m, so runChat falls through to
		// one-shot mode after resume runs; a working OneShotFn keeps that
		// fall-through from panicking on tests that only care about the
		// resume half.
		OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
			return OneShotResult{ThreadID: req.Thread, TurnID: "t1", Content: "ok"}, nil
		},
	})
	t.Cleanup(func() { SetClient(nil) })

	c := NewChatCommand()
	var outBuf, errBuf bytes.Buffer
	c.SetOut(&outBuf)
	c.SetErr(&errBuf)
	c.SetIn(&bytes.Buffer{})
	c.SetContext(context.Background())
	c.SetArgs(argv)
	err := c.Execute()
	return got, outBuf.String(), errBuf.String(), err
}

// TestResumeTopicFlag_RealCommand proves --resume-topic reaches the real
// Client.Resume call with the id, and that the composed context is
// written to stdout.
func TestResumeTopicFlag_RealCommand(t *testing.T) {
	got, stdout, _, err := runResumeCommand(t, func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{ThreadID: "th1", LeadingContext: "composed block here", Tokens: 10}, nil
	}, "--resume-topic", "th1", "-m", "continue please")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.TopicID != "th1" {
		t.Fatalf("Resume called with %+v, want TopicID=th1", got)
	}
	if !strings.Contains(stdout, "composed block here") || !strings.Contains(stdout, "(10 tokens)") {
		t.Fatalf("stdout = %q, want it to contain the composed context", stdout)
	}
}

// TestResumeQueryFlag_ThreadForwarded proves --thread <id> combined with
// --resume-query sends Thread=<id>: the destination session thread, whose
// stored marker the daemon reads as the target tier.
func TestResumeQueryFlag_ThreadForwarded(t *testing.T) {
	got, _, _, err := runResumeCommand(t, func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{LeadingContext: "q result"}, nil
	}, "--thread", "existing-thread", "--resume-query", "budget planning", "-m", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Query != "budget planning" || got.Thread != "existing-thread" {
		t.Fatalf("Resume called with %+v, want Query set and Thread=existing-thread", got)
	}
}

// TestResumeFlags_BothNamed_UsageError proves --resume-topic and
// --resume-query together refuse before the client is ever called.
func TestResumeFlags_BothNamed_UsageError(t *testing.T) {
	called := false
	_, _, _, err := runResumeCommand(t, func(context.Context, ResumeRequest) (ResumeResult, error) {
		called = true
		return ResumeResult{}, nil
	}, "--resume-topic", "t1", "--resume-query", "q1", "-m", "hi")
	if !errors.Is(err, errResumeFlagsConflict) || !strings.Contains(err.Error(), errResumeFlagsConflict.Error()) {
		t.Fatalf("Execute err = %v, want errResumeFlagsConflict", err)
	}
	if called {
		t.Fatal("Client.Resume was called despite the usage error")
	}
}

// TestResumeResult_TrimReportWrittenToStderr proves TrimReport lines land
// on stderr as "warning: ..." lines, never on stdout mixed into the
// context block.
func TestResumeResult_TrimReportWrittenToStderr(t *testing.T) {
	_, stdout, stderr, err := runResumeCommand(t, func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{LeadingContext: "kept content", Trimmed: []string{"turn:dropped: budget exceeded"}}, nil
	}, "--resume-topic", "th1", "-m", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(stdout, "warning:") {
		t.Fatalf("stdout = %q, must not contain the warning line", stdout)
	}
	if !strings.Contains(stderr, "warning: turn:dropped: budget exceeded") {
		t.Fatalf("stderr = %q, want the trim-report warning line", stderr)
	}
}

// TestResumeResult_JSONUsesWireNames proves --json renders the contract's
// field names {thread_id, leading_context, tokens, trimmed}, with trimmed
// [] (not null) when nothing was trimmed.
func TestResumeResult_JSONUsesWireNames(t *testing.T) {
	_, stdout, _, err := runResumeCommand(t, func(context.Context, ResumeRequest) (ResumeResult, error) {
		return ResumeResult{ThreadID: "th1", LeadingContext: "block", Tokens: 3}, nil
	}, "--resume-topic", "th1", "--json", "-m", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	first := strings.SplitN(stdout, "\n", 2)[0]
	want := `{"thread_id":"th1","leading_context":"block","tokens":3,"trimmed":[]}`
	if first != want {
		t.Fatalf("--json resume line = %s, want %s", first, want)
	}
}

// TestResumeFlagsAndSlashCommandShareOnePath, TestResume_LeadingContext_
// PrependedToNextTurn and TestResumeFlag_ErrorPropagates live in
// resume_flag_test.go.
