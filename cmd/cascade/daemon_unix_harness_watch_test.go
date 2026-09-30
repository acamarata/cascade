//go:build !windows

// Purpose: TestHarnessWatchReceivesFleetSessions (P1-E12-W6-S121-T1 AC2):
//
//	the harness session watch, wired by the production
//	wireHarnessSessionWatch (RegisterHarnessSessionWatch over
//	claudeSessionStream), receives a fleet.sessions record the REAL daemon
//	serves through GET /events?topic=fleet.sessions on its own socket, and
//	turns it into a cascade-claude lifecycle event.
//
//	The daemon runs the real platformDaemonRun lifecycle
//	(startFleetSmokeDaemon, fleet_daemon_smoke_test.go). The watch under
//	test publishes to a bus this test owns: the daemon's own bus sits
//	behind its store's exclusive lock and cannot be read while it runs,
//	while every hop that matters (dial, topic dispatch, sessions SSE
//	stream, record fold, lifecycle translation) is the production path.
//
// SPORT: cmd/cascade:harness-watch (coverage-only addition).
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/plugins/claude"
)

// claudeWatchNamespace mirrors internal/plugins' unexported namespace the
// cascade-claude watch publishes lifecycle events to.
const claudeWatchNamespace = "plugins.claude.sessions"

func TestHarnessWatchReceivesFleetSessions(t *testing.T) {
	const sessionID = "harness-watch-1"
	runDeps := startFleetSmokeDaemon(t, sessionID)

	clock := runtime.SystemClock{}
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := bus.Subscribe(ctx, claudeWatchNamespace, "harness-watch-test", 16)
	if err != nil {
		cancel()
		t.Fatalf("Subscribe: %v", err)
	}

	manifest := daemon.NewManifest(slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	wireHarnessSessionWatch(ctx, manifest, bus, runDeps.Paths.SocketPath())
	t.Cleanup(func() {
		cancel()
		manifest.Wait()
	})

	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatal("watch event subscription closed before the seeded session arrived")
			}
			var payload struct {
				SessionID string `json:"session_id"`
				State     string `json:"state"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("lifecycle payload: %v (%q)", err, ev.Payload)
			}
			if payload.SessionID != sessionID {
				continue
			}
			if ev.Kind != events.EventKind(claude.LifecycleStarted) || payload.State != string(claude.SessionRunning) {
				t.Fatalf("lifecycle event = %s/%s, want %s/%s", ev.Kind, payload.State, claude.LifecycleStarted, claude.SessionRunning)
			}
			return
		case <-timeout:
			t.Fatalf("harness watch never received the daemon's fleet.sessions record for %q", sessionID)
		}
	}
}
