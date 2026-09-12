package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// resetClient clears the package-level Client seam after a test so
// SetClient calls never leak between tests (they run with -race, and
// sync.RWMutex already serializes access, but a leaked fake could still
// make a LATER test observe the wrong behavior).
func resetClient(t *testing.T) {
	t.Helper()
	SetClient(nil)
	t.Cleanup(func() { SetClient(nil) })
}

// TestChatOneShot: `cascade chat "hello"` sends one turn, streams the
// response, exits 0 (nil error). Verifies exact stdout content: the
// default two-line header form plus content.
func TestChatOneShot(t *testing.T) {
	resetClient(t)
	SetClient(fakeClient{OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
		if req.Prompt != "hello" {
			t.Fatalf("OneShot: prompt = %q, want %q", req.Prompt, "hello")
		}
		return OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "hi there"}, nil
	}})

	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hello"}, fakeEnv{}.lookup)
	if err != nil {
		t.Fatalf("runChat: %v", err)
	}
	got := c.OutOrStdout().(interface{ String() string }).String()
	want := "thread: th1\nturn: t1\nhi there\n"
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// TestChatOneShotMessageFlagAliasesPositional: -m behaves identically to
// the positional prompt form (07-CLI-COMMAND-TREE; U/S-46.T4 relies on
// this).
func TestChatOneShotMessageFlagAliasesPositional(t *testing.T) {
	resetClient(t)
	var gotPrompt string
	SetClient(fakeClient{OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
		gotPrompt = req.Prompt
		return OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "ok"}, nil
	}})

	c := newTestCobraCommand()
	c.SetArgs([]string{"-m", "hello"})
	root := NewChatCommand()
	root.SetOut(c.OutOrStdout())
	root.SetContext(context.Background())
	root.SetArgs([]string{"-m", "hello"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPrompt != "hello" {
		t.Fatalf("prompt via -m = %q, want %q", gotPrompt, "hello")
	}
}

// TestChatOneShotThreadContinuity: --thread continues an existing thread.
func TestChatOneShotThreadContinuity(t *testing.T) {
	resetClient(t)
	var gotThread string
	SetClient(fakeClient{OneShotFn: func(_ context.Context, req OneShotRequest) (OneShotResult, error) {
		gotThread = req.Thread
		return OneShotResult{TurnID: "t2", ThreadID: req.Thread, Content: "follow-up reply"}, nil
	}})

	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "follow-up", thread: "existing-thread"}, fakeEnv{}.lookup)
	if err != nil {
		t.Fatalf("runChat: %v", err)
	}
	if gotThread != "existing-thread" {
		t.Fatalf("thread = %q, want %q", gotThread, "existing-thread")
	}
}

// TestChatOneShotJSON: --json emits {turn_id, thread_id, content}.
func TestChatOneShotJSON(t *testing.T) {
	resetClient(t)
	SetClient(fakeClient{OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
		return OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "hi"}, nil
	}})

	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hello", json: true}, fakeEnv{}.lookup)
	if err != nil {
		t.Fatalf("runChat: %v", err)
	}
	var got struct {
		TurnID   string `json:"turn_id"`
		ThreadID string `json:"thread_id"`
		Content  string `json:"content"`
	}
	out := c.OutOrStdout().(interface{ String() string }).String()
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.TurnID != "t1" || got.ThreadID != "th1" || got.Content != "hi" {
		t.Fatalf("decoded = %+v, want {t1 th1 hi}", got)
	}
}

// TestChatOneShotQuiet: --quiet suppresses the metadata header lines.
func TestChatOneShotQuiet(t *testing.T) {
	resetClient(t)
	SetClient(fakeClient{OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
		return OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "hi"}, nil
	}})

	c := newTestCobraCommand()
	if err := runChat(c, chatOptions{prompt: "hello", quiet: true}, fakeEnv{}.lookup); err != nil {
		t.Fatalf("runChat: %v", err)
	}
	got := c.OutOrStdout().(interface{ String() string }).String()
	if got != "hi\n" {
		t.Fatalf("stdout = %q, want %q", got, "hi\n")
	}
}

// TestChatDaemonUnreachable: with no Client configured, one-shot mode
// returns the typed KindUnavailable error rather than hanging or
// panicking.
func TestChatDaemonUnreachable(t *testing.T) {
	resetClient(t)
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hello"}, fakeEnv{}.lookup)
	if err == nil {
		t.Fatal("runChat: want an error, got nil")
	}
	if !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("runChat: err = %v, want errClientUnconfigured", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("Kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestChatThreadNotFound: a typed KindNotFound error from the Client
// propagates through runChat unchanged (never swallowed, never
// downgraded to a generic failure).
func TestChatThreadNotFound(t *testing.T) {
	resetClient(t)
	wantErr := cascade.New(cascade.KindNotFound, "cascade chat: thread \"missing\" not found")
	SetClient(fakeClient{OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
		return OneShotResult{}, wantErr
	}})

	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hi", thread: "missing"}, fakeEnv{}.lookup)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runChat: err = %v, want %v", err, wantErr)
	}
}

// TestChatMalformedSSEFrame: a Stream that reports a malformed-frame error
// surfaces through the TUI path as a typed error rendered into the
// transcript, never a panic and never silently dropped. Exercised at the
// pumpStream level directly (no tea.Program, no TTY).
func TestChatMalformedSSEFrame(t *testing.T) {
	tokens := make(chan string)
	errs := make(chan error, 1)
	close(tokens)
	wantErr := cascade.New(cascade.KindInvalidInput, "cascade chat: malformed SSE frame")
	errs <- wantErr
	close(errs)

	client := fakeClient{StreamFn: func(context.Context, OneShotRequest) (<-chan string, <-chan error) {
		return tokens, errs
	}}

	rec := &recordingSender{}
	pumpStream(context.Background(), rec, client, OneShotRequest{Prompt: "hi"})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(rec.msgs))
	}
	em, ok := rec.msgs[0].(streamErrMsg)
	if !ok {
		t.Fatalf("message type = %T, want streamErrMsg", rec.msgs[0])
	}
	if !errors.Is(em.err, wantErr) {
		t.Fatalf("streamErrMsg.err = %v, want %v", em.err, wantErr)
	}
}

// TestChatNoInputNoPrompt: CASCADE_NO_INPUT=1 with no prompt and no -m
// exits non-zero with a clear error (06 §5 rule 8 / 08-INIT-CONFIG-SPEC).
func TestChatNoInputNoPrompt(t *testing.T) {
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{}, fakeEnv{"CASCADE_NO_INPUT": "1"}.lookup)
	if !errors.Is(err, errChatNoInputNoPrompt) {
		t.Fatalf("runChat: err = %v, want errChatNoInputNoPrompt", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// TestChatNoInputWithPromptRunsHeadlessly: CASCADE_NO_INPUT=1 with a
// prompt (positional or -m) runs headlessly and exits 0 — the automation
// parity guard only blocks the TUI path, never one-shot mode.
func TestChatNoInputWithPromptRunsHeadlessly(t *testing.T) {
	resetClient(t)
	SetClient(fakeClient{OneShotFn: func(context.Context, OneShotRequest) (OneShotResult, error) {
		return OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "ok"}, nil
	}})
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hello"}, fakeEnv{"CASCADE_NO_INPUT": "1"}.lookup)
	if err != nil {
		t.Fatalf("runChat: %v", err)
	}
}

// TestChatTUIRefusesWhenDaemonUnreachable: `cascade chat` with no prompt
// and an unconfigured Client refuses immediately with a typed error
// WITHOUT ever constructing a tea.Program — proof there is no blank
// screen, no hang, and no silent retry loop. This is the TTY-free proof
// for the "no arg" path: runChat never reaches tea.NewProgram in this
// case, so the test needs no TTY and completes instantly.
func TestChatTUIRefusesWhenDaemonUnreachable(t *testing.T) {
	resetClient(t)
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{}, fakeEnv{}.lookup)
	if !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("runChat: err = %v, want errClientUnconfigured", err)
	}
}

// TestWriteOneShotResultDefaultForm locks in the non-JSON, non-quiet
// rendering shape independent of runChat, so a future refactor of
// writeOneShotResult cannot silently change the on-wire text contract.
func TestWriteOneShotResultDefaultForm(t *testing.T) {
	c := newTestCobraCommand()
	if err := writeOneShotResult(c, OneShotResult{TurnID: "t", ThreadID: "th", Content: "c"}, chatOptions{}); err != nil {
		t.Fatalf("writeOneShotResult: %v", err)
	}
	got := c.OutOrStdout().(interface{ String() string }).String()
	if !strings.HasPrefix(got, "thread: th\nturn: t\n") {
		t.Fatalf("stdout = %q, missing expected header", got)
	}
}
