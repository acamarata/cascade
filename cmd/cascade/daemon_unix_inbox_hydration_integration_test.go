//go:build !windows && integration

package main

// Purpose: the hydration hook over a LIVE unix socket (integration lane): the
// real client presents the owner marker, the daemon's access policy turns it
// into the caller the drain rule needs, and the envelope reaches the hook.
// Constraints: a unix socket under os.MkdirTemp (no TCP), a fixed clock.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// serveInboxDaemon serves the inbox registry over a unix socket and returns
// the socket dialer.
func serveInboxDaemon(t *testing.T, paths runtime.PathProvider, store provider.Store, clock runtime.Clock) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	bus := events.New(storetest.NewMemStore(), clock)
	registry := inboxRegistry(t, paths, store, clock, bus)
	sockDir, err := os.MkdirTemp("", "hydsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.NewRPCServer(registry, rpc.NewSSEHandler(bus, "daemon", func(events.EventKind) bool { return false }, clock))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
}

func TestHydrationHookOverSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	clock := hydrationTestClock{now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	paths, store := doctorTestPaths(t), storetest.NewMemStore()
	deps := contextScopeDeps{Paths: paths, Getenv: func(string) string { return "" }, Environ: func() []string { return nil },
		DialContext: serveInboxDaemon(t, paths, store, clock)}
	stdin, sid := capturedPrompt(t, t.TempDir())
	c := client.New(paths.SocketPath(), client.DialFunc(deps.DialContext), 5*time.Second)
	start := hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionStart, SessionID: sid, PID: 1, TimestampMs: clock.now.UnixMilli()}
	if err := c.Do(context.Background(), hookpacks.MethodHookEvent, start, nil); err != nil {
		t.Fatalf("SessionStart hook event: %v", err)
	}
	deliverAddressed(t, store, clock, sid)
	got := runHook(t, deps, stdin)
	if !strings.Contains(got, `<cascade-inbox-data provenance="untrusted-source">`) || !strings.Contains(got, `- id="hook-env-1" class="addressed"`) {
		t.Fatalf("the socket hook output lacks the envelope:\n%s", got)
	}
	if again := runHook(t, deps, stdin); strings.Contains(again, "hook-env-1") {
		t.Fatalf("the envelope was delivered twice:\n%s", again)
	}
}
