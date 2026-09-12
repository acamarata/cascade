package cmd

// Shared test doubles for chat_test.go, chat_tui_test.go, and (on the
// Windows leg only) chat_windows_test.go. No build tag: this file compiles
// on every platform, since Windows's own tests reuse fakeEnv/
// newTestCobraCommand rather than duplicating them.

import (
	"bytes"
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
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

// fakeClient is a Client test double. OneShotFn/StreamFn are optional;
// a nil OneShotFn/StreamFn panics if called, which surfaces a test bug
// (calling a path the test did not intend to exercise) immediately rather
// than silently returning a zero value.
type fakeClient struct {
	OneShotFn func(ctx context.Context, req OneShotRequest) (OneShotResult, error)
	StreamFn  func(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error)
}

func (f fakeClient) OneShot(ctx context.Context, req OneShotRequest) (OneShotResult, error) {
	return f.OneShotFn(ctx, req)
}

func (f fakeClient) Stream(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error) {
	return f.StreamFn(ctx, req)
}
