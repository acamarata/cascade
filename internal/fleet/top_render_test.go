package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderFrame_EmptySnapshot(t *testing.T) {
	snap := NewTopSnapshot(nil, GovernorPanel{}, TaskPanel{}, time.Unix(0, 0))
	got := RenderFrame(snap, 80, 24)
	for _, want := range []string{"Sessions", "unavailable: no resource sample yet", "unavailable: no active ticket", "sessions=0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderFrame missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderFrame_FullSnapshot(t *testing.T) {
	snap := NewTopSnapshot(
		[]TopSessionRow{{SessionID: "sess-1", Harness: "claude", State: "running", Lane: "lane-a", Account: "a1", Elapsed: "5m0s"}},
		GovernorPanel{Available: true, CPUFraction: 0.5, MemUsedBytes: 1 << 20, MemTotalBytes: 2 << 20, QueueDepth: 2},
		TaskPanel{Available: true, TicketID: "P1-E18-W4-S40-T1", LastEvent: "step-done"},
		time.Unix(0, 0),
	)
	got := RenderFrame(snap, 100, 30)
	for _, want := range []string{"sess-1", "claude", "lane-a", "cpu=50%", "queue=2", "P1-E18-W4-S40-T1", "step-done", "sessions=1", "lanes_busy=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderFrame missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderFrame_Deterministic(t *testing.T) {
	snap := NewTopSnapshot(
		[]TopSessionRow{{SessionID: "sess-1", State: "stalled"}},
		GovernorPanel{Available: true}, TaskPanel{}, time.Unix(0, 0),
	)
	a := RenderFrame(snap, 80, 24)
	b := RenderFrame(snap, 80, 24)
	if a != b {
		t.Fatalf("RenderFrame is not deterministic:\n%s\n---\n%s", a, b)
	}
	if strings.Contains(a, "\x1b[") {
		t.Fatalf("RenderFrame emitted ANSI escape codes despite the forced-ascii renderer:\n%q", a)
	}
}

func TestRenderFrame_ZeroWidthDefaults(_ *testing.T) {
	snap := NewTopSnapshot(nil, GovernorPanel{}, TaskPanel{}, time.Unix(0, 0))
	// must not panic on a zero/negative width.
	_ = RenderFrame(snap, 0, 0)
	_ = RenderFrame(snap, -5, -5)
}

func TestRenderSnapshotJSON_RoundTrips(t *testing.T) {
	snap := NewTopSnapshot(
		[]TopSessionRow{{SessionID: "sess-1", State: "running"}},
		GovernorPanel{Available: true, CPUFraction: 0.75},
		TaskPanel{Available: true, TicketID: "P1-X"},
		time.Unix(123, 0).UTC(),
	)
	data, err := RenderSnapshotJSON(snap)
	if err != nil {
		t.Fatalf("RenderSnapshotJSON: %v", err)
	}
	var got TopSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Summary != snap.Summary || got.Governor != snap.Governor || len(got.Sessions) != 1 {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, snap)
	}
	if !got.GeneratedAt.Equal(snap.GeneratedAt) {
		t.Fatalf("GeneratedAt mismatch: got %v want %v", got.GeneratedAt, snap.GeneratedAt)
	}
}
