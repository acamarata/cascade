//go:build !windows

// Purpose: in-process proofs for the completion-check command: it is mounted
//
//	on the real command tree as a hidden verb under `fleet` and adds no root
//	noun; its flags are validated before any daemon call; stop_hook_active is
//	read from decoded JSON, never by substring; every refusal is one line
//	with exit code 2; and a stdin that never closes cannot hold it past its
//	deadline. The live-daemon cases dial a real daemon through the production
//	dialer, so the request/decode path here is the one the binary runs.
//
// SPORT: cmd/cascade/fleet-completion-check (coverage for P1-CI-09).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runCompletionCheckCmd executes the real command against socket with env as
// the process environment and stdin as the harness JSON.
func runCompletionCheckCmd(env map[string]string, stdin string, args ...string) error {
	deps := productionFleetSessionsDeps()
	deps.Getenv = func(k string) string { return env[k] }
	cmd := newFleetCompletionCheckCmd(deps)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestFleetCompletionCheck_IsAHiddenFleetVerbAndNoNewRootNoun(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"fleet", "completion-check"})
	if err != nil || cmd.Name() != "completion-check" || !cmd.Hidden {
		t.Fatalf("fleet completion-check = %v (err=%v), want a hidden command", cmd.Name(), err)
	}
	if got, _, _ := root.Find([]string{"completion-check"}); got != root {
		t.Fatalf("completion-check resolves at the root as %q, want no root noun", got.Name())
	}
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"fleet", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fleet --help: %v", err)
	}
	if strings.Contains(help.String(), "completion-check") {
		t.Fatalf("fleet --help lists the hidden command:\n%s", help.String())
	}
}

func TestFleetCompletionCheck_LiveDaemonVerdicts(t *testing.T) {
	capture := &completionCapture{}
	sock, stop := startCompletionDaemon(t, capture.register)
	env := map[string]string{"CASCADE_SESSION_ID": "sess-1", "CASCADE_JOB_ID": "job-1", "CASCADE_TICKET_ID": "tkt-1"}
	args := []string{"--event", "TaskCompleted", "--socket", sock}

	if err := runCompletionCheckCmd(env, `{"session_id":"x"}`, args...); err != nil {
		t.Fatalf("allow: %v", err)
	}
	var payload hookpacks.CompletionHookPayload
	got := capture.received()
	if len(got) != 1 || json.Unmarshal(got[0], &payload) != nil {
		t.Fatalf("daemon saw %d requests, want 1 decodable one", len(got))
	}
	want := hookpacks.CompletionHookPayload{EventType: hookpacks.EventTaskCompleted, SessionID: "sess-1", JobID: "job-1", TaskID: "tkt-1"}
	if payload != want {
		t.Fatalf("payload = %+v, want %+v", payload, want)
	}

	capture.mu.Lock()
	capture.reply = hookpacks.CompletionHookResponse{Deny: true, Reason: "first\nsecond"}
	capture.mu.Unlock()
	err := runCompletionCheckCmd(env, "", args...)
	if err == nil || cascade.ExitCode(err) != 2 || !strings.Contains(err.Error(), "first second") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("deny: err=%v exit=%d, want a one-line exit-2 refusal carrying the reason", err, cascade.ExitCode(err))
	}

	stop()
	err = runCompletionCheckCmd(env, "", args...)
	if err == nil || cascade.ExitCode(err) != 2 || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("stopped daemon: err=%v exit=%d, want a request-failed exit-2 refusal", err, cascade.ExitCode(err))
	}
}

func TestFleetCompletionCheck_StopHookActiveComesFromDecodedJSON(t *testing.T) {
	capture := &completionCapture{}
	sock, _ := startCompletionDaemon(t, capture.register)
	cases := []struct {
		name, stdin string
		want        bool
	}{
		{"compact_true", `{"stop_hook_active":true}`, true},
		{"spaced_true", "{ \"session_id\" : \"s\",\n \"stop_hook_active\" : true }", true},
		{"reordered_true", `{"transcript_path":"/t","stop_hook_active":true,"hook_event_name":"Stop"}`, true},
		{"false", `{"stop_hook_active":false}`, false},
		{"lookalike_inside_string", `{"note":"\"stop_hook_active\":true"}`, false},
		{"nested_member", `{"inner":{"stop_hook_active":true}}`, false},
		{"string_true", `{"stop_hook_active":"true"}`, false},
		{"not_json", `stop_hook_active:true`, false},
		{"empty", ``, false},
		{"oversized", `{"pad":"` + strings.Repeat("a", completionCheckMaxStdin) + `","stop_hook_active":true}`, false},
	}
	for _, tc := range cases {
		before := len(capture.received())
		if err := runCompletionCheckCmd(nil, tc.stdin, "--event", "Stop", "--socket", sock); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := capture.received()
		var payload hookpacks.CompletionHookPayload
		if len(got) != before+1 || json.Unmarshal(got[len(got)-1], &payload) != nil {
			t.Fatalf("%s: daemon saw %d new requests, want 1", tc.name, len(got)-before)
		}
		if payload.StopHookActive != tc.want {
			t.Fatalf("%s: stop_hook_active = %v, want %v", tc.name, payload.StopHookActive, tc.want)
		}
	}
}

func TestFleetCompletionCheck_FlagsAreValidatedBeforeAnyCall(t *testing.T) {
	capture := &completionCapture{}
	sock, _ := startCompletionDaemon(t, capture.register)
	for _, args := range [][]string{
		{"--socket", sock},
		{"--event", "Bogus", "--socket", sock},
		{"--event", "stop", "--socket", sock},
		{"--event", "Stop"},
		{"--event", "Stop", "--socket", sock, "extra"},
		{"--event", "Stop", "--socket", sock, "--nope"},
	} {
		err := runCompletionCheckCmd(nil, "", args...)
		if err == nil || cascade.ExitCode(err) != 2 {
			t.Fatalf("args %v: err=%v exit=%d, want an exit-2 refusal", args, err, cascade.ExitCode(err))
		}
	}
	if n := len(capture.received()); n != 0 {
		t.Fatalf("daemon saw %d requests from invalid invocations, want 0", n)
	}
}

func TestCompletionRefusal_IsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("é", completionCheckMaxReason)
	for name, in := range map[string]string{
		"newlines": "a\nb\r\nc", "tabs_and_bell": "a\tb\x07c\x7fd", "long": long, "invalid_utf8": "ok\xff\xfe" + long,
	} {
		err := completionRefusal(in)
		msg := err.Error()
		if strings.ContainsAny(msg, "\n\r\x07\x7f") || !utf8.ValidString(msg) || len(msg) > completionCheckMaxReason+64 {
			t.Fatalf("%s: refusal %q is not one bounded valid line", name, msg[:min(len(msg), 80)])
		}
		if cascade.ExitCode(err) != 2 {
			t.Fatalf("%s: exit = %d, want 2", name, cascade.ExitCode(err))
		}
	}
}

func TestCompletionRefusal_ReplacesExtendedControls(t *testing.T) {
	controls := []rune{'\u2028', '\u2029'}
	for r := rune(0x7f); r <= 0x9f; r++ {
		controls = append(controls, r)
	}
	for _, r := range controls {
		err := completionRefusal("before" + string(r) + "after")
		want := cascade.New(cascade.KindInvalidInput, "before after")
		if err.Error() != want.Error() || cascade.ExitCode(err) != 2 {
			t.Errorf("U+%04X: refusal=%q exit=%d, want %q exit=2", r, err, cascade.ExitCode(err), want)
		}
	}
}

func TestReadStopHookActive_ReservesRequestBudget(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if readStopHookActive(ctx, pr) {
		t.Fatal("unread stdin reported stop_hook_active=true")
	}
	if took := time.Since(start); took < 900*time.Millisecond || took > 2*time.Second || ctx.Err() != nil {
		t.Fatalf("stdin took %s, context=%v; want a one-second read with request budget remaining", took, ctx.Err())
	}
}

func TestFleetCompletionCheck_OpenStdinStillCallsDaemon(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	capture := &completionCapture{}
	sock, _ := startCompletionDaemon(t, capture.register)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := newFleetCompletionCheckCmd(productionFleetSessionsDeps())
	cmd.SetIn(pr)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--event", "Stop", "--socket", sock})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("open stdin prevented daemon allow: %v", err)
	}
	var payload hookpacks.CompletionHookPayload
	got := capture.received()
	if len(got) != 1 || json.Unmarshal(got[0], &payload) != nil || payload.StopHookActive {
		t.Fatalf("daemon received %s, want one request with stop_hook_active=false", got)
	}
}

func TestCompletionVerdict_OnlyAnExplicitFalseAllows(t *testing.T) {
	yes, no := true, false
	if err := completionVerdict(completionCheckReply{Deny: &no, Reason: "ignored"}); err != nil {
		t.Fatalf("deny=false: %v", err)
	}
	for name, reply := range map[string]completionCheckReply{
		"absent": {}, "absent_with_reason": {Reason: "r"}, "true": {Deny: &yes}, "true_with_reason": {Deny: &yes, Reason: "r"},
	} {
		if err := completionVerdict(reply); err == nil || cascade.ExitCode(err) != 2 {
			t.Fatalf("%s: err=%v, want an exit-2 refusal", name, err)
		}
	}
}

func TestReadStopHookActive_AbandonsAStdinThatNeverCloses(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if readStopHookActive(ctx, pr) {
		t.Fatal("an unread stdin reported stop_hook_active=true")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("readStopHookActive held for %s past a 100ms deadline", took)
	}
}
