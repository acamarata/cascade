//go:build !windows

// Purpose: tests for `cascade fleet sessions hook-event`
//
//	(contract:harness-session-registration). Every case drives the real cobra
//	command against a LIVE daemon: the real runtime store over t.TempDir(), the
//	real RPC server buildRPCServer composes, served by daemon.Run on a real unix
//	socket, with fleet.sessions.hook_event mounted through the same
//	hookpacks.RegisterHookEventHandler the production wiring will call. State is
//	asserted by listing the daemon's stored sessions, never by an event.
//
//	The stdin inputs are the CAPTURED harness payloads under
//	internal/fleet/hookpacks/testdata/cc-hook-fixtures (provenance in that
//	directory's README); none is authored here.
//
// SPORT: cmd/cascade/fleet-sessions-hook (coverage for P1-CORE-14).
package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

const hookFixtureDir = "../../internal/fleet/hookpacks/testdata/cc-hook-fixtures"

// hookTestPPID is the parent pid the injected env reports.
const hookTestPPID = 4242

// loadHookFixture returns one captured harness payload, byte for byte.
func loadHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(hookFixtureDir, name+".json")) //nolint:gosec // fixed test fixture path.
	if err != nil {
		t.Fatalf("read captured fixture %s: %v", name, err)
	}
	return raw
}

// hookTestEnv is the injected process facts: the system clock (the daemon
// range-checks the timestamp against its own clock) and a fixed parent pid.
func hookTestEnv() sessionHookEnv {
	return sessionHookEnv{Clock: runtime.NewSystemClock(), Getppid: func() int { return hookTestPPID }}
}

// startHookEventDaemon serves a live daemon over a throwaway cascade home and
// returns the deps that dial it. fleet.sessions.hook_event is mounted by an
// rpcServerOption, the same seam withPolicyHandlers uses.
func startHookEventDaemon(t *testing.T) fleetSessionsDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runDeps := newRunTestDeps(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	_, paths, settings, err := loadDaemonConfig(ctx, runDeps)
	if err != nil {
		t.Fatalf("loadDaemonConfig: %v", err)
	}
	store, _, closeStore, err := openRuntimeStore(ctx, paths, runDeps.Clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	bus := events.New(store, runDeps.Clock)
	mount := rpcServerOption{register: func(registry *rpc.Registry) error {
		return hookpacks.RegisterHookEventHandler(registry, sessions.New(store, runDeps.Clock, bus), bus, runDeps.Clock)
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, manifest, connections, err := buildRPCServer(bus, runDeps.Clock, logger, settings, paths, nil, store, mount)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.RunOptions{
			Settings: settings, PIDPath: daemon.PIDFilePath(paths), Logger: logger, Clock: runDeps.Clock,
			Server: server, Environ: runDeps.Environ, Manifest: manifest, Connections: connections,
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		closeStore()
	})
	waitForSocket(t, paths.SocketPath())
	deps := productionFleetSessionsDeps()
	deps.Paths = runDeps.Paths
	return deps
}

// waitForSocket blocks until the daemon socket answers, or fails the test.
func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if socketDialable(socket) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daemon socket never became dialable")
}

// hookRun is one hook-event invocation's observable result.
type hookRun struct {
	err    error
	stdout string
	stderr string
}

// runHookEvent executes `hook-event <args...>` with stdin and the injected env.
func runHookEvent(deps fleetSessionsDeps, stdin []byte, args ...string) hookRun {
	cmd := newSessionsHookEventCmd(deps, hookTestEnv())
	var out, errBuf bytes.Buffer
	cmd.SetIn(bytes.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return hookRun{err: err, stdout: out.String(), stderr: errBuf.String()}
}

// listLiveSessions reads the daemon's stored sessions over its socket.
func listLiveSessions(t *testing.T, deps fleetSessionsDeps) []sessions.SessionRecord {
	t.Helper()
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		t.Fatalf("ResolveSettings: %v", err)
	}
	c := client.New(settings.SocketPath, client.UnixDialer, 5*time.Second)
	records, err := sessions.NewClient(c).List(context.Background(), sessions.Filter{})
	if err != nil {
		t.Fatalf("fleet.sessions.list: %v", err)
	}
	return records
}

// fixtureSessionID returns the session_id the captured fixture carries.
func fixtureSessionID(t *testing.T, name string) string {
	t.Helper()
	in, ok := readSessionHookInput(bytes.NewReader(loadHookFixture(t, name)))
	if !ok || in.SessionID == "" {
		t.Fatalf("captured fixture %s has no readable session_id", name)
	}
	return in.SessionID
}

func TestSessionsHookEventForwardsSessionID(t *testing.T) {
	deps := startHookEventDaemon(t)
	wantID := fixtureSessionID(t, "sessionstart")

	res := runHookEvent(deps, loadHookFixture(t, "sessionstart"), "SessionStart")
	if res.err != nil || res.stdout != "" || res.stderr != "" {
		t.Fatalf("hook-event SessionStart: err=%v stdout=%q stderr=%q, want a silent success", res.err, res.stdout, res.stderr)
	}
	records := listLiveSessions(t, deps)
	if len(records) != 1 {
		t.Fatalf("daemon holds %d sessions after SessionStart, want exactly 1: %+v", len(records), records)
	}
	got := records[0]
	if got.SessionID != wantID || got.State != sessions.StateActive.String() ||
		got.Harness != sessionHookHarness || got.PID != hookTestPPID {
		t.Fatalf("stored session = %+v, want id %q, state %q, harness %q, pid %d",
			got, wantID, sessions.StateActive.String(), sessionHookHarness, hookTestPPID)
	}
}

// TestSessionsHookEventPreFixBodyFailsValidate reproduces the failure this
// command replaced: the old curl template posted a fixed body carrying only
// event_type, and the same live daemon refused it in validate(), so no session
// was stored.
func TestSessionsHookEventPreFixBodyFailsValidate(t *testing.T) {
	deps := startHookEventDaemon(t)
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		t.Fatal(err)
	}
	c := client.New(settings.SocketPath, client.UnixDialer, 5*time.Second)
	fixedBody := map[string]string{"event_type": string(hookpacks.EventSessionStart)}
	err = c.Do(context.Background(), hookpacks.MethodHookEvent, fixedBody, nil)
	if err == nil {
		t.Fatal("the pre-fix fixed body was accepted by the daemon, want a validate() refusal")
	}
	if want := hookpacks.ErrUnknownHarness.Error(); !strings.Contains(err.Error(), want) {
		t.Fatalf("pre-fix body error = %q, want it to carry validate()'s %q", err.Error(), want)
	}
	if n := len(listLiveSessions(t, deps)); n != 0 {
		t.Fatalf("daemon stored %d sessions from the pre-fix body, want 0", n)
	}
}

func TestSessionsHookEventRejectsBadInput(t *testing.T) {
	deps := startHookEventDaemon(t)
	start := loadHookFixture(t, "sessionstart")
	// Seed one real session so "the store is unchanged" is compared against a
	// non-empty store, never against two empty lists.
	if res := runHookEvent(deps, start, "SessionStart"); res.err != nil || res.stderr != "" {
		t.Fatalf("seed SessionStart: %+v", res)
	}
	before := listLiveSessions(t, deps)
	if len(before) != 1 {
		t.Fatalf("seeded store holds %d sessions, want 1", len(before))
	}
	seededID := before[0].SessionID
	if before[0].State != sessions.StateActive.String() {
		t.Fatalf("seeded session state = %q, want %q", before[0].State, sessions.StateActive.String())
	}

	cases := []struct {
		name  string
		args  []string
		stdin []byte
	}{
		{"empty stdin", []string{"SessionStart"}, nil},
		{"non-JSON stdin", []string{"SessionStart"}, []byte("not json at all")},
		{"JSON that is not an object", []string{"SessionStart"}, []byte(`["SessionStart"]`)},
		{"over the 1 MiB cap by one byte", []string{"SessionStart"}, paddedHookInput("SessionStart", "padded-id", sessionHookMaxStdin+1)},
		{"mismatched hook_event_name", []string{"SessionStart"}, loadHookFixture(t, "pretooluse")},
		{"id with a path separator", []string{"SessionStart"}, hookInputWithID("SessionStart", "../etc/passwd")},
		{"id with a newline", []string{"SessionStart"}, hookInputWithID("SessionStart", "abc\\ndef")},
		{"id of 129 bytes", []string{"SessionStart"}, hookInputWithID("SessionStart", strings.Repeat("a", 129))},
		{"empty id", []string{"SessionStart"}, hookInputWithID("SessionStart", "")},
		{"unknown event argument", []string{"Notification"}, hookInputWithID("Notification", "valid-id")},
		{"allowlist: SubagentStop for the seeded session", []string{"SubagentStop"}, hookInputWithID("SubagentStop", seededID)},
		{"no event argument", nil, loadHookFixture(t, "sessionstart")},
		{"unknown flag --bogus", []string{"--bogus"}, loadHookFixture(t, "sessionstart")},
		{"help flag --help", []string{"--help"}, loadHookFixture(t, "sessionstart")},
		{"help flag -h", []string{"-h"}, loadHookFixture(t, "sessionstart")},
		{"flag after a valid event", []string{"Stop", "--x"}, loadHookFixture(t, "stop")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runHookEvent(deps, tc.stdin, tc.args...)
			if res.err != nil {
				t.Fatalf("exit status: err = %v, want nil (always exit 0)", res.err)
			}
			if res.stdout != "" || res.stderr != "" {
				t.Fatalf("stdout=%q stderr=%q, want both empty for input that is refused before sending", res.stdout, res.stderr)
			}
			if after := listLiveSessions(t, deps); !reflect.DeepEqual(before, after) {
				t.Fatalf("daemon store changed:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// TestSessionsHookEventAcceptsPayloadAtCap is the boundary control for the
// over-cap case above: a payload of exactly 1 MiB is still read and sent.
func TestSessionsHookEventAcceptsPayloadAtCap(t *testing.T) {
	deps := startHookEventDaemon(t)
	res := runHookEvent(deps, paddedHookInput("SessionStart", "at-cap-id", sessionHookMaxStdin), "SessionStart")
	if res.err != nil || res.stdout != "" || res.stderr != "" {
		t.Fatalf("hook-event at the cap: %+v", res)
	}
	records := listLiveSessions(t, deps)
	if len(records) != 1 || records[0].SessionID != "at-cap-id" {
		t.Fatalf("stored sessions = %+v, want the one at-cap-id session", records)
	}
}

// hookInputWithID builds a minimal harness-shaped payload for one id.
func hookInputWithID(event, id string) []byte {
	return []byte(`{"session_id":"` + id + `","hook_event_name":"` + event + `"}`)
}

// paddedHookInput returns a valid JSON payload of exactly size bytes, padded
// with trailing whitespace, so the size check is what decides its fate.
func paddedHookInput(event, id string, size int) []byte {
	base := hookInputWithID(event, id)
	return append(base, bytes.Repeat([]byte(" "), size-len(base))...)
}
