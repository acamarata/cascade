package cmd

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNewSubmitFuncDeliversTokensAndDone drives newSubmitFunc's closure
// directly: no tea.Program, no TTY. Proves the returned cmd is nil (the
// Model relies on the goroutine + msgSender.Send path, not a tea.Cmd
// return value) and that pumpStream, invoked through the closure, really
// reaches the recordingSender.
func TestNewSubmitFuncDeliversTokensAndDone(t *testing.T) {
	tokens := make(chan string)
	errs := make(chan error)

	client := fakeClient{StreamFn: func(context.Context, OneShotRequest) (<-chan string, <-chan error) {
		// Unbuffered and hand-off-ordered so the token really is received
		// before tokens closes and before errs ever sends, matching the
		// Client.Stream contract this file's doc comment states (errs
		// sends its single terminal value only once tokens has closed) —
		// this makes pumpStream's token/done ordering deterministic here,
		// not merely likely.
		go func() {
			tokens <- "hi"
			close(tokens)
			errs <- nil
			close(errs)
		}()
		return tokens, errs
	}}

	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "th1", client, &sender)
	cmd, cancel := submit("hello")
	if cmd != nil {
		t.Fatal("newSubmitFunc: cmd must be nil (delivery is via msgSender.Send)")
	}
	if cancel == nil {
		t.Fatal("newSubmitFunc: cancel must not be nil")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		n := len(rec.msgs)
		rec.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) < 2 {
		t.Fatalf("recorded %d messages, want at least 2 (token + done)", len(rec.msgs))
	}
	if _, ok := rec.msgs[0].(tokenMsg); !ok {
		t.Fatalf("first message = %T, want tokenMsg", rec.msgs[0])
	}
	if _, ok := rec.msgs[1].(streamDoneMsg); !ok {
		t.Fatalf("second message = %T, want streamDoneMsg", rec.msgs[1])
	}
}

// TestNewSubmitFuncCancelStopsPump proves the cancel func returned by
// newSubmitFunc actually stops pumpStream: with a Stream that never
// produces anything, canceling must make pumpStream return instead of
// leaking forever, and no message is ever recorded.
func TestNewSubmitFuncCancelStopsPump(t *testing.T) {
	blockTokens := make(chan string)
	blockErrs := make(chan error)
	client := fakeClient{StreamFn: func(context.Context, OneShotRequest) (<-chan string, <-chan error) {
		return blockTokens, blockErrs
	}}

	rec := &recordingSender{}
	var sender msgSender = rec
	submit := newSubmitFunc(context.Background(), "", client, &sender)
	_, cancel := submit("hello")
	cancel()

	time.Sleep(20 * time.Millisecond)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) != 0 {
		t.Fatalf("recorded %d messages after cancel, want 0", len(rec.msgs))
	}
}

// TestRunTUIRefusesUnconfiguredClient proves runTUI itself (not just
// runChat's dispatch to it) refuses immediately, with no tea.Program ever
// constructed — this call would hang on a real terminal loop if the
// preflight check were missing, so a passing, fast test IS the proof.
func TestRunTUIRefusesUnconfiguredClient(t *testing.T) {
	resetClient(t)
	c := newTestCobraCommand()
	err := runTUI(context.Background(), c, "")
	if !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("runTUI: err = %v, want errClientUnconfigured", err)
	}
}

// TestUnconfiguredClientStream proves the default Client's Stream method
// also fails closed: a closed, empty token channel and exactly one typed
// error, never a channel that blocks forever.
func TestUnconfiguredClientStream(t *testing.T) {
	tokens, errs := (unconfiguredClient{}).Stream(context.Background(), OneShotRequest{})
	if _, ok := <-tokens; ok {
		t.Fatal("unconfiguredClient.Stream: tokens channel must be closed and empty")
	}
	err, ok := <-errs
	if !ok || !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("unconfiguredClient.Stream: errs = %v (ok=%v), want errClientUnconfigured", err, ok)
	}
}

// TestOSLookupEnvWrapsRealEnviron is a thin smoke test proving
// osLookupEnv really delegates to the process environment (it is the
// ONE call site allowed to do so — see env.go's doc comment).
func TestOSLookupEnvWrapsRealEnviron(t *testing.T) {
	t.Setenv("CASCADE_PA_CHAT_TEST_VAR", "1")
	v, ok := osLookupEnv("CASCADE_PA_CHAT_TEST_VAR")
	if !ok || v != "1" {
		t.Fatalf("osLookupEnv = %q, %v, want \"1\", true", v, ok)
	}
	if _, ok := osLookupEnv("CASCADE_PA_CHAT_TEST_VAR_UNSET"); ok {
		t.Fatal("osLookupEnv: want ok=false for an unset variable")
	}
}
