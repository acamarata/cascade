package daemon

// Purpose: unit tests for status.go's status.get handler: every response
//   field is populated from real, injected state (not a stub), the cancel-
//   path returns a typed error rather than panicking, and Health reflects
//   the real Manifest instead of always reporting "ok".
// SPORT: internal/daemon (ADD, per T-1 sport_updates).

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestStatusGet_FieldsFromLiveState(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := runtime.NewFixedClock(start)
	clock.Advance(2500 * time.Millisecond)

	var active int64
	atomic.StoreInt64(&active, 3)

	manifest := NewManifest(nil, clock)
	manifest.Register("ipc-socket")
	manifest.Started("ipc-socket", "/tmp/daemon.sock")

	provider := NewStatusProvider(clock, start, "/tmp/daemon.sock", &active, manifest)
	result, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: unexpected error: %v", err)
	}

	resp, ok := result.(StatusResponse)
	if !ok {
		t.Fatalf("Handler result is %T, want StatusResponse", result)
	}

	if resp.Daemon.PID != os.Getpid() {
		t.Errorf("PID = %d, want the real process pid %d", resp.Daemon.PID, os.Getpid())
	}
	if resp.Daemon.UptimeS != 2.5 {
		t.Errorf("UptimeS = %v, want 2.5 (derived from the injected clock)", resp.Daemon.UptimeS)
	}
	if resp.Daemon.Connections != 3 {
		t.Errorf("Connections = %d, want 3 (the shared live counter)", resp.Daemon.Connections)
	}
	if resp.Daemon.SocketPath != "/tmp/daemon.sock" {
		t.Errorf("SocketPath = %q, want %q", resp.Daemon.SocketPath, "/tmp/daemon.sock")
	}
	if resp.Health != "ok" {
		t.Errorf("Health = %q, want %q (manifest has no failed/disabled/skipped subsystem)", resp.Health, "ok")
	}
	if resp.Version == "" {
		t.Error("Version is empty, want buildinfo.Version")
	}
}

// TestStatusGet_ConnectionsAdvancesLive proves Connections tracks the
// SAME shared counter live, not a value copied once at construction - the
// exact property that distinguishes a real wiring from a one-time
// snapshot.
func TestStatusGet_ConnectionsAdvancesLive(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	var active int64
	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", &active, nil)

	first, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := first.(StatusResponse).Daemon.Connections; got != 0 {
		t.Fatalf("Connections (before) = %d, want 0", got)
	}

	atomic.AddInt64(&active, 5)

	second, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := second.(StatusResponse).Daemon.Connections; got != 5 {
		t.Fatalf("Connections (after) = %d, want 5 (the shared counter advanced)", got)
	}
}

// TestStatusGet_NilConnectionsReportsZero proves a nil counter reports 0
// honestly rather than panicking - the documented "not wired in" case.
func TestStatusGet_NilConnectionsReportsZero(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, nil)

	result, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := result.(StatusResponse).Daemon.Connections; got != 0 {
		t.Fatalf("Connections = %d, want 0", got)
	}
}

// TestStatusGet_HealthDegradesOnFailedSubsystem proves Health is a real,
// non-constant signal: a failed subsystem in the SAME Manifest Run itself
// drives makes status.get report "degraded", not a hardcoded "ok".
func TestStatusGet_HealthDegradesOnFailedSubsystem(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	manifest := NewManifest(nil, clock)
	manifest.Register("ipc-socket")
	manifest.Failed("ipc-socket", "listen: address already in use")

	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, manifest)
	result, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := result.(StatusResponse).Health; got != "degraded" {
		t.Fatalf("Health = %q, want %q", got, "degraded")
	}
}

// TestStatusGet_HealthDegradesOnDisabledOrSkipped covers the other two
// non-ok manifest states.
func TestStatusGet_HealthDegradesOnDisabledOrSkipped(t *testing.T) {
	cases := []struct {
		name string
		set  func(m *Manifest)
	}{
		{"disabled", func(m *Manifest) { m.Disabled("sub", "config-gated off") }},
		{"skipped", func(m *Manifest) { m.Skipped("sub", "platform refusal") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := runtime.NewFixedClock(time.Now())
			manifest := NewManifest(nil, clock)
			manifest.Register("sub")
			tc.set(manifest)

			provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, manifest)
			result, err := provider.Handler()(context.Background(), nil)
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			if got := result.(StatusResponse).Health; got != "degraded" {
				t.Fatalf("Health = %q, want %q", got, "degraded")
			}
		})
	}
}

// TestStatusGet_NilManifestReportsOK covers the documented nil-manifest
// fallback (only reachable from a test that omits it).
func TestStatusGet_NilManifestReportsOK(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, nil)
	result, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := result.(StatusResponse).Health; got != "ok" {
		t.Fatalf("Health = %q, want %q", got, "ok")
	}
}

// TestStatusGet_CancelledContext proves the handler returns a typed
// KindCanceled error, not a panic, when its context is already cancelled.
func TestStatusGet_CancelledContext(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := provider.Handler()(ctx, nil)
	if err == nil {
		t.Fatal("Handler: expected an error for a cancelled context, got nil")
	}
	if result != nil {
		t.Fatalf("Handler: expected a nil result alongside the error, got %#v", result)
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindCanceled {
		t.Fatalf("Handler error kind = %v (ok=%v), want KindCanceled", kind, ok)
	}
}

// TestStatusMethod_MatchesTheWireNameInTheSpec pins the method name to a
// literal rather than to the constant.
//
// Every other test refers to StatusMethod, so all of them move together if
// the constant is renamed and none of them would notice. The name is a wire
// contract: an external client sends this exact string, so renaming it
// breaks callers that this repo's tests cannot see. Comparing the constant
// against a hard-coded literal is the one place that has to spell it out.
func TestStatusMethod_MatchesTheWireNameInTheSpec(t *testing.T) {
	const wireName = "status.get"
	if StatusMethod != wireName {
		t.Errorf("StatusMethod = %q, want %q; this is a wire contract and renaming it breaks external callers",
			StatusMethod, wireName)
	}
}
