//go:build !windows

// Purpose: the two end-to-end proofs for `cascade fleet sessions hook-event`
//
//	that need the whole live-daemon harness from fleet_sessions_hook_test.go:
//	the hook command is silent on stdout on every path (and says exactly one
//	thing on stderr when delivery fails), and a session moves through its whole
//	lifecycle when the captured SessionStart, PreToolUse, Stop and SessionEnd
//	payloads go through the command, read back through `cascade fleet
//	sessions --json`.
//
// SPORT: cmd/cascade/fleet-sessions-hook (coverage for P1-CORE-14).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
)

// silentPathCase is one hook-event invocation whose stdout must stay empty.
type silentPathCase struct {
	name       string
	deps       fleetSessionsDeps
	args       []string
	stdin      []byte
	wantStderr bool
	unchanged  bool // refused before sending: the live store must not move
}

// silentPathCases lists every path through the command: delivered, refused
// before sending (including flags and an unlisted event for the live id), and
// failed delivery.
func silentPathCases(t *testing.T, live, dead fleetSessionsDeps) []silentPathCase {
	start := loadHookFixture(t, "sessionstart")
	stop := loadHookFixture(t, "stop")
	seededID := fixtureSessionID(t, "sessionstart")
	return []silentPathCase{
		{"delivered", live, []string{"SessionStart"}, start, false, false},
		{"refused: empty stdin", live, []string{"SessionStart"}, nil, false, true},
		{"refused: non-JSON", live, []string{"Stop"}, []byte("{"), false, true},
		{"refused: unknown event", live, []string{"Bogus"}, stop, false, true},
		{"refused: SubagentStop for the active session", live, []string{"SubagentStop"}, hookInputWithID("SubagentStop", seededID), false, true},
		{"refused: --bogus", live, []string{"--bogus"}, start, false, true},
		{"refused: --help", live, []string{"--help"}, start, false, true},
		{"refused: -h", live, []string{"-h"}, start, false, true},
		{"refused: flag after a valid event", live, []string{"Stop", "--x"}, stop, false, true},
		{"refused: hostile id", live, []string{"Stop"}, hookInputWithID("Stop", "a b"), false, true},
		{"daemon refuses: unknown session", live, []string{"PreToolUse"}, hookInputWithID("PreToolUse", "never-started"), true, false},
		{"daemon unreachable", dead, []string{"SessionStart"}, start, true, false},
	}
}

func TestSessionsHookEventSilentStdout(t *testing.T) {
	live := startHookEventDaemon(t)
	// A daemon-less deps: a cascade home with nothing listening on its socket.
	dead := productionFleetSessionsDeps()
	dead.Paths = fakeDaemonPaths{root: filepath.Join(t.TempDir(), "none")}
	const prefix = "cascade: fleet session hook not delivered:"
	for _, tc := range silentPathCases(t, live, dead) {
		t.Run(tc.name, func(t *testing.T) {
			var before []sessions.SessionRecord
			if tc.unchanged {
				before = listLiveSessions(t, live)
				if len(before) != 1 || before[0].State != sessions.StateActive.String() {
					t.Fatalf("precondition: live store = %+v, want the one active session", before)
				}
			}
			res := runHookEvent(tc.deps, tc.stdin, tc.args...)
			if tc.unchanged {
				if after := listLiveSessions(t, live); !reflect.DeepEqual(before, after) {
					t.Fatalf("live store changed by a refused input:\nbefore %+v\nafter  %+v", before, after)
				}
			}
			if res.err != nil {
				t.Fatalf("exit status: err = %v, want nil (never non-zero, never 2)", res.err)
			}
			if res.stdout != "" {
				t.Fatalf("stdout = %q, want empty on every path", res.stdout)
			}
			if !tc.wantStderr {
				if res.stderr != "" {
					t.Fatalf("stderr = %q, want empty when nothing failed to deliver", res.stderr)
				}
				return
			}
			if !strings.HasPrefix(res.stderr, prefix) || strings.Count(res.stderr, "\n") != 1 || !strings.HasSuffix(res.stderr, "\n") {
				t.Fatalf("stderr = %q, want exactly one line starting %q", res.stderr, prefix)
			}
			if kind := strings.TrimSpace(strings.TrimPrefix(res.stderr, prefix)); kind == "" {
				t.Fatalf("stderr = %q names no error kind", res.stderr)
			}
		})
	}
}

// fleetSessionsJSON runs `cascade fleet sessions --json` against the live
// daemon and returns the rows it prints.
func fleetSessionsJSON(t *testing.T, deps fleetSessionsDeps) []fleetSessionRow {
	t.Helper()
	cmd, buf := newTestFleetCmd(deps)
	cmd.SetContext(runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false}))
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cascade fleet sessions --json: %v", err)
	}
	var envelope struct {
		OK   bool              `json:"ok"`
		Data []fleetSessionRow `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&envelope); err != nil || !envelope.OK {
		t.Fatalf("decode fleet sessions --json output %q: ok=%v err=%v", buf.String(), envelope.OK, err)
	}
	return envelope.Data
}

func TestLiveSessionLifecycleThroughHookPack(t *testing.T) {
	deps := startHookEventDaemon(t)
	id := fixtureSessionID(t, "sessionstart")
	for _, name := range []string{"pretooluse", "stop", "sessionend"} {
		if got := fixtureSessionID(t, name); got != id {
			t.Fatalf("captured fixture %s carries session %q, want the one captured session %q", name, got, id)
		}
	}
	steps := []struct {
		fixture, event, wantState string
	}{
		{"sessionstart", "SessionStart", sessions.StateActive.String()},
		{"pretooluse", "PreToolUse", sessions.StateActive.String()},
		{"stop", "Stop", sessions.StateIdle.String()},
		{"sessionend", "SessionEnd", sessions.StateClosed.String()},
	}
	for i, step := range steps {
		res := runHookEvent(deps, loadHookFixture(t, step.fixture), step.event)
		if res.err != nil || res.stdout != "" || res.stderr != "" {
			t.Fatalf("step %d %s: %+v, want a silent success", i, step.event, res)
		}
		rows := fleetSessionsJSON(t, deps)
		if len(rows) != 1 || rows[0].SessionID != id || rows[0].State != step.wantState {
			t.Fatalf("after %s: fleet sessions --json = %+v, want one session %q in state %q", step.event, rows, id, step.wantState)
		}
		if step.event == "PreToolUse" {
			assertSessionTouched(t, deps, id)
		}
	}
}

// assertSessionTouched proves the tool event touched the stored record: the
// table has no tool columns, so the daemon's own list is the witness.
func assertSessionTouched(t *testing.T, deps fleetSessionsDeps, id string) {
	t.Helper()
	records := listLiveSessions(t, deps)
	if len(records) != 1 || records[0].SessionID != id {
		t.Fatalf("stored sessions = %+v, want the one session %q", records, id)
	}
	if records[0].ToolCount != 1 || records[0].LastToolAt == nil {
		t.Fatalf("after PreToolUse tool_count=%d last_tool_at=%v, want 1 and set", records[0].ToolCount, records[0].LastToolAt)
	}
}
