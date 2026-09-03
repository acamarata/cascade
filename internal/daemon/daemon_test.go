package daemon

// Purpose: platform-independent unit tests: pidfile encode/round-trip,
//   classifyPID's four-way liveness decision (the stale-vs-recycled bug
//   class this ticket's brief calls out by name), Settings resolution from
//   *runtime.Config.Extra (including the "no default asserted" contract
//   requirement for shutdown_grace), and the fail-loud subsystem Manifest
//   (R-14.87) — all pure logic, no real process/socket/signal, so this
//   file has no build tag and runs identically on every platform including
//   the Windows CI lane (R-14.131).
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// --- pidfile round-trip ---

func TestPIDFile_WriteReadRemove(t *testing.T) {
	path := t.TempDir() + "/daemon.pid"
	want := pidRecord{PID: 4242, StartedAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}

	if err := writePIDFile(path, want); err != nil {
		t.Fatalf("writePIDFile: %v", err)
	}
	got, ok, err := readPIDFile(path)
	if err != nil || !ok {
		t.Fatalf("readPIDFile: ok=%v err=%v", ok, err)
	}
	if got.PID != want.PID || !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("readPIDFile = %+v, want %+v", got, want)
	}
	if err := removePIDFile(path); err != nil {
		t.Fatalf("removePIDFile: %v", err)
	}
	if _, ok, err := readPIDFile(path); ok || err != nil {
		t.Errorf("after remove: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	// Idempotent: removing again must not error.
	if err := removePIDFile(path); err != nil {
		t.Errorf("second removePIDFile: %v", err)
	}
}

func TestReadPIDFile_MissingIsNotAnError(t *testing.T) {
	_, ok, err := readPIDFile(t.TempDir() + "/nope.pid")
	if ok || err != nil {
		t.Errorf("missing pidfile: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestReadPIDFile_CorruptIsIntegrityError(t *testing.T) {
	path := t.TempDir() + "/daemon.pid"
	if err := writeFileHelper(path, "not json"); err != nil {
		t.Fatal(err)
	}
	_, _, err := readPIDFile(path)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("corrupt pidfile err = %v, want KindIntegrity", err)
	}
}

// --- classifyPID: the stale-vs-recycled decision ---

type fakeProber struct {
	alive     map[int]bool
	startTime map[int]time.Time
}

func (f fakeProber) IsAlive(pid int) bool { return f.alive[pid] }
func (f fakeProber) StartTime(pid int) (time.Time, bool) {
	t, ok := f.startTime[pid]
	return t, ok
}

func TestClassifyPID(t *testing.T) {
	base := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		rec   pidRecord
		ok    bool
		prb   fakeProber
		state livenessState
	}{
		{"no pidfile at all", pidRecord{}, false, fakeProber{}, livenessNotRunning},
		{
			"process gone (stale)", pidRecord{PID: 1, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{1: false}}, livenessStale,
		},
		{
			"alive, start time matches", pidRecord{PID: 2, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{2: true}, startTime: map[int]time.Time{2: base}},
			livenessRunning,
		},
		{
			"alive, start time within tolerance", pidRecord{PID: 3, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{3: true}, startTime: map[int]time.Time{3: base.Add(time.Second)}},
			livenessRunning,
		},
		{
			"alive, PID recycled by an unrelated process", pidRecord{PID: 4, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{4: true}, startTime: map[int]time.Time{4: base.Add(time.Hour)}},
			livenessRecycled,
		},
		{
			"alive, start time unknown: best-effort running", pidRecord{PID: 5, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{5: true}}, livenessRunning,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyPID(c.rec, c.ok, c.prb)
			if got != c.state {
				t.Errorf("classifyPID() = %v, want %v", got, c.state)
			}
		})
	}
}

// --- Settings resolution ---

// resolveSettingsCase is one TestResolveSettings table row.
type resolveSettingsCase struct {
	name        string
	cfg         *runtime.Config
	wantSocket  string
	wantGrace   time.Duration
	wantSet     bool
	wantErrKind cascade.Kind
}

func resolveSettingsCases() []resolveSettingsCase {
	return []resolveSettingsCase{
		{
			name: "no daemon section: falls back to PathProvider socket, grace unset",
			cfg:  &runtime.Config{}, wantSocket: "/tmp/socket-default.sock",
		},
		{
			name: "socket overridden, shutdown_grace as duration string",
			cfg: &runtime.Config{Extra: map[string]interface{}{
				"daemon": map[string]interface{}{"socket": "/custom.sock", "shutdown_grace": "5s"},
			}},
			wantSocket: "/custom.sock", wantGrace: 5 * time.Second, wantSet: true,
		},
		{
			name: "shutdown_grace as bare TOML integer seconds",
			cfg: &runtime.Config{Extra: map[string]interface{}{
				"daemon": map[string]interface{}{"shutdown_grace": int64(3)},
			}},
			wantSocket: "/tmp/socket-default.sock", wantGrace: 3 * time.Second, wantSet: true,
		},
		{
			name: "malformed shutdown_grace is a typed InvalidInput error",
			cfg: &runtime.Config{Extra: map[string]interface{}{
				"daemon": map[string]interface{}{"shutdown_grace": "not-a-duration"},
			}},
			wantErrKind: cascade.KindInvalidInput,
		},
		{name: "nil config", cfg: nil, wantSocket: "/tmp/socket-default.sock"},
	}
}

func TestResolveSettings(t *testing.T) {
	paths := fakePathsFor(t, "/tmp/socket-default.sock")
	for _, c := range resolveSettingsCases() {
		t.Run(c.name, func(t *testing.T) {
			s, err := ResolveSettings(c.cfg, paths)
			if c.wantErrKind != 0 {
				if !cascade.HasKind(err, c.wantErrKind) {
					t.Errorf("err = %v, want kind %v", err, c.wantErrKind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.SocketPath != c.wantSocket || s.GraceSet != c.wantSet || s.ShutdownGrace != c.wantGrace {
				t.Errorf("got %+v, want socket=%q grace=%v set=%v", s, c.wantSocket, c.wantGrace, c.wantSet)
			}
		})
	}
}

// --- fail-loud subsystem Manifest (R-14.87) ---

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
