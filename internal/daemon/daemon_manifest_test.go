package daemon

// Purpose: the fail-loud subsystem Manifest (R-14.87) — split out of
//   daemon_test.go under R-14.117/R-14.133 (Art.10.3's 300-line file cap;
//   daemon_test.go grew past it once this ticket added the classifyPID
//   negative-diff and ResolveSettings absent-key cases). Same package, no
//   behaviour change; recordingHandler/newRecordingLogger stay usable from
//   every other test file in this package (daemon_run_test.go,
//   daemon_unix_stop_test.go) exactly as before the split.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// recordingHandler captures every slog.Record it receives, so tests can
// assert on structured fields without depending on a specific text/JSON
// rendering.
type recordingHandler struct {
	records *[]slog.Record
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

func newRecordingLogger() (*slog.Logger, *[]slog.Record) {
	records := &[]slog.Record{}
	return slog.New(recordingHandler{records: records}), records
}

func TestManifest_StartedLogsInfoLine(t *testing.T) {
	log, records := newRecordingLogger()
	m := NewManifest(log, runtime.NewFixedClock(time.Now()))
	m.Register("ipc-socket")
	m.Started("ipc-socket", "/tmp/daemon.sock")

	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != SubsystemRunning || snap[0].Detail != "/tmp/daemon.sock" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(*records) != 1 || (*records)[0].Level != slog.LevelInfo {
		t.Fatalf("records = %+v, want exactly one INFO line", *records)
	}
}

// TestManifest_FailedSubsystem_ErrorLineAndState is this ticket's required
// "kill one subsystem's startup in test" case (AC): a subsystem's start
// attempt fails, and the manifest must show BOTH a distinct ERROR-level
// log line AND SubsystemError state — absence of a log line is never the
// only evidence (R-14.87).
func TestManifest_FailedSubsystem_ErrorLineAndState(t *testing.T) {
	log, records := newRecordingLogger()
	m := NewManifest(log, runtime.NewFixedClock(time.Now()))
	m.Register("event-bus") // declared, then killed before it could start
	m.Failed("event-bus", "bind: address already in use")

	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != SubsystemError {
		t.Fatalf("snapshot = %+v, want SubsystemError", snap)
	}
	if snap[0].Detail != "bind: address already in use" {
		t.Errorf("detail = %q", snap[0].Detail)
	}
	if len(*records) != 1 || (*records)[0].Level != slog.LevelError {
		t.Fatalf("records = %+v, want exactly one ERROR line", *records)
	}
}

func TestManifest_DisabledAndSkipped_AlsoLogErrorLevel(t *testing.T) {
	log, records := newRecordingLogger()
	m := NewManifest(log, runtime.NewFixedClock(time.Now()))
	m.Register("mcp-socket")
	m.Disabled("mcp-socket", "config-gated off")
	m.Register("sse-bridge")
	m.Skipped("sse-bridge", "platform refusal")

	for _, r := range *records {
		if r.Level != slog.LevelError {
			t.Errorf("record %q level = %v, want Error", r.Message, r.Level)
		}
	}
	snap := m.Snapshot()
	if snap[0].State != SubsystemDisabled || snap[1].State != SubsystemSkipped {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestManifest_RegisterTwice_IsNoop(t *testing.T) {
	m := NewManifest(nil, runtime.NewFixedClock(time.Now()))
	m.Register("ipc-socket")
	m.Started("ipc-socket", "addr")
	m.Register("ipc-socket") // must not reset state back to Declared
	if snap := m.Snapshot(); snap[0].State != SubsystemRunning {
		t.Errorf("state = %v, want unchanged Running", snap[0].State)
	}
}

func TestManifest_Snapshot_PreservesRegistrationOrder(t *testing.T) {
	m := NewManifest(nil, runtime.NewFixedClock(time.Now()))
	m.Register("c")
	m.Register("a")
	m.Register("b")
	snap := m.Snapshot()
	got := []string{snap[0].Name, snap[1].Name, snap[2].Name}
	want := []string{"c", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Snapshot order = %v, want %v", got, want)
		}
	}
}
