package cmd

// Shared test doubles for chat_test.go, chat_tui_test.go, and (on the
// Windows leg only) chat_windows_test.go. No build tag: this file compiles
// on every platform, since Windows's own tests reuse fakeEnv/
// newTestCobraCommand rather than duplicating them.

import (
	"bytes"
	"context"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingSender is a msgSender double: it records every tea.Msg passed
// to Send under a mutex, with no tea.Program, no TTY, and no real
// terminal involved — the exact "TESTABLE WITHOUT A TTY" shape
// pumpStream's tests need.
type recordingSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *recordingSender) Send(msg tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
}

// fakeEnv is an envLookup double: no entry is ever "present" unless
// explicitly set, so tests never depend on the real process environment.
type fakeEnv map[string]string

func (f fakeEnv) lookup(key string) (string, bool) {
	v, ok := f[key]
	return v, ok
}

// newTestCobraCommand returns a bare *cobra.Command with its own
// bytes.Buffer out/err streams and a background context — exactly the
// shape runChat/runTUI expect, with no TTY and no dependency on the real
// os.Stdout/os.Stdin.
func newTestCobraCommand() *cobra.Command {
	c := &cobra.Command{Use: "chat"}
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetIn(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

// fakeClient is a Client test double. OneShotFn/StreamFn/ResumeFn are
// optional; a nil one panics if called, surfacing a test that reached a
// path it did not intend -- EXCEPT CreateThreadFn, which defaults to
// minting a fresh "thread-"-prefixed id (the daemon's chat.create_thread
// shape), because every resume on a new session creates its thread first.
type fakeClient struct {
	OneShotFn      func(ctx context.Context, req OneShotRequest) (OneShotResult, error)
	StreamFn       func(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error)
	ResumeFn       func(ctx context.Context, req ResumeRequest) (ResumeResult, error)
	CreateThreadFn func(ctx context.Context, privacyMode string) (string, error)
}

func (f fakeClient) OneShot(ctx context.Context, req OneShotRequest) (OneShotResult, error) {
	return f.OneShotFn(ctx, req)
}

func (f fakeClient) Stream(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error) {
	return f.StreamFn(ctx, req)
}

func (f fakeClient) Resume(ctx context.Context, req ResumeRequest) (ResumeResult, error) {
	return f.ResumeFn(ctx, req)
}

func (f fakeClient) CreateThread(ctx context.Context, privacyMode string) (string, error) {
	if f.CreateThreadFn == nil {
		id, err := cascade.NewID()
		if err != nil {
			return "", err
		}
		return "thread-" + strings.ToLower(string(id)), nil
	}
	return f.CreateThreadFn(ctx, privacyMode)
}
