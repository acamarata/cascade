// Purpose: tests for the hidden `cascade hook relay <event>` command.
//
// Constraints: no real driver, environment or uid is used; the relay is
//
//	injected, and the only real-root checks are in-memory cobra lookups and
//	help rendering. The real relay against real trees is in
//	hook_relay_tree_test.go.
//
// SPORT: cmd/cascade root mounts (P1-AGT-14, contract:agent-hook-relay).
package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// relayTestNow is the injected clock's instant; the deadline must derive
// from it, not from the wall clock.
var relayTestNow = time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)

// TestHookRelayCmdHidden proves `hook` and `hook relay` are hidden, are absent
// from `cascade --help`, and still resolve.
func TestHookRelayCmdHidden(t *testing.T) {
	out, err := execRoot(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	if strings.Contains(out, "hook") {
		t.Fatalf("--help must not list the hook noun:\n%s", out)
	}
	root := newRootCmd()
	relay, _, err := root.Find([]string{"hook", "relay"})
	if err != nil || relay.Name() != "relay" {
		t.Fatalf("hook relay does not resolve: %v", err)
	}
	if !relay.Hidden || !relay.Parent().Hidden {
		t.Fatalf("hook relay and its parent must be hidden (relay=%t parent=%t)", relay.Hidden, relay.Parent().Hidden)
	}
	if !strings.Contains(string(renderRootNouns()), "hook hidden=true aliases=\n") {
		t.Fatal("root noun record must list hook as hidden")
	}
}

// relayEnvStub records what the command passes to the relay.
type relayEnvStub struct {
	cwd, event string
	uid        int
	stdin      string
	calls      int
	deadline   time.Time
	err        error
}

func (s *relayEnvStub) env() hookRelayEnv {
	return hookRelayEnv{
		Getenv: func(k string) string {
			if k == projectDirVar {
				return "/work/tree"
			}
			return ""
		},
		Getuid: func() int { return 501 },
		Relay: func(ctx context.Context, cwd, event string, stdin io.Reader, uid int) error {
			s.calls++
			s.cwd, s.event, s.uid = cwd, event, uid
			b, _ := io.ReadAll(stdin)
			s.stdin = string(b)
			s.deadline, _ = ctx.Deadline()
			return s.err
		},
		Clock:   runtime.NewFixedClock(relayTestNow),
		Timeout: time.Minute,
	}
}

func runRelayCmd(t *testing.T, env hookRelayEnv, stdin string, args ...string) error {
	t.Helper()
	cmd := newHookRelayCmd(env)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestHookRelayCmdPassesProcessFactsAndStdin(t *testing.T) {
	s := &relayEnvStub{}
	if err := runRelayCmd(t, s.env(), `{"x":1}`, "PostToolUse"); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if s.calls != 1 || s.cwd != "/work/tree" || s.event != "PostToolUse" || s.uid != 501 || s.stdin != `{"x":1}` {
		t.Fatalf("relay saw %+v", s)
	}
	if s.deadline.IsZero() {
		t.Fatal("relay must run under a deadline")
	}
}

func TestHookRelayCmdRejectsWrongArgCountWithoutRelaying(t *testing.T) {
	for _, args := range [][]string{nil, {"PreToolUse", "extra"}, {"--help"}} {
		s := &relayEnvStub{}
		if len(args) == 1 {
			// DisableFlagParsing: --help is one ordinary argument (an event name).
			if err := runRelayCmd(t, s.env(), "", args...); err != nil || s.event != "--help" {
				t.Fatalf("--help must be passed through as the event: err=%v event=%q", err, s.event)
			}
			continue
		}
		err := runRelayCmd(t, s.env(), "", args...)
		if err == nil || s.calls != 0 {
			t.Fatalf("args %v: err=%v calls=%d", args, err, s.calls)
		}
		if code := cascade.ExitCode(err); code == 0 || code == cascade.ExitInvalidInput {
			t.Fatalf("args %v exit code %d must be non-zero and never 2 (PreToolUse blocks on 2)", args, code)
		}
	}
}

func TestHookRelayCmdNeverExitsTwo(t *testing.T) {
	s := &relayEnvStub{err: cascade.New(cascade.KindInvalidInput, "bad payload")}
	err := runRelayCmd(t, s.env(), "", "Stop")
	if err == nil || cascade.ExitCode(err) == cascade.ExitInvalidInput {
		t.Fatalf("an input error must not map to exit 2: %v (code %d)", err, cascade.ExitCode(err))
	}
}

func TestHookRelayCmdRefusalIsNonZeroAndNoOpIsZero(t *testing.T) {
	denied := &relayEnvStub{err: cascade.New(cascade.KindPermissionDenied, "refused")}
	if err := runRelayCmd(t, denied.env(), "", "PreToolUse"); err == nil || cascade.ExitCode(err) == 0 {
		t.Fatalf("a refusal must exit non-zero: %v", err)
	}
	if err := runRelayCmd(t, (&relayEnvStub{}).env(), "", "PreToolUse"); err != nil {
		t.Fatalf("a no-op relay must exit 0: %v", err)
	}
}

// TestHookRelayCmdNoProjectDirIsNoop: without CLAUDE_PROJECT_DIR the hook is
// outside a spawn; it exits 0 and the relay never runs.
func TestHookRelayCmdNoProjectDirIsNoop(t *testing.T) {
	s := &relayEnvStub{}
	env := s.env()
	env.Getenv = func(string) string { return "" }
	if err := runRelayCmd(t, env, `{"x":1}`, "Stop"); err != nil || s.calls != 0 {
		t.Fatalf("err=%v calls=%d, want exit 0 and no relay", err, s.calls)
	}
}

// TestHookRelayCmdRelativeProjectDirRefused: a relative project directory
// reaches the relay, which refuses it; the exit is non-zero and never 2.
func TestHookRelayCmdRelativeProjectDirRefused(t *testing.T) {
	env := (&relayEnvStub{}).env()
	env.Getenv = func(string) string { return "work/tree" }
	env.Relay = agents.Relay
	err := runRelayCmd(t, env, "", "Stop")
	if code := cascade.ExitCode(err); err == nil || code == cascade.ExitInvalidInput {
		t.Fatalf("a relative project directory: err=%v exit %d, want non-zero and never 2", err, code)
	}
}

func TestHookRelayCmdDeadlineFromInjectedClock(t *testing.T) {
	s := &relayEnvStub{}
	if err := runRelayCmd(t, s.env(), "", "Stop"); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if want := relayTestNow.Add(time.Minute); !s.deadline.Equal(want) {
		t.Fatalf("deadline %v, want %v (the injected clock plus the timeout)", s.deadline, want)
	}
}

func TestHookRelayCmdPanicIsInternalNotTwo(t *testing.T) {
	env := (&relayEnvStub{}).env()
	env.Relay = func(context.Context, string, string, io.Reader, int) error { panic("relay bug") }
	err := runRelayCmd(t, env, "", "PreToolUse")
	if k, _ := cascade.KindOf(err); k != cascade.KindInternal || cascade.ExitCode(err) != cascade.ExitInternal {
		t.Fatalf("a panic must surface as KindInternal (exit %d), got %v (exit %d)", cascade.ExitInternal, err, cascade.ExitCode(err))
	}
	if want := cascade.KindInternal.String() + ": hook relay: internal failure"; err.Error() != want || strings.Contains(err.Error(), "relay bug") {
		t.Fatalf("the error must be the fixed message and must not echo the panic value: %q", err.Error())
	}
}
