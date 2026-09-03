package daemon

// Purpose: platform-independent unit tests: pidfile encode/round-trip,
//   classifyPID's four-way liveness decision (the stale-vs-recycled bug
//   class this ticket's brief calls out by name), and Settings resolution
//   from *runtime.Config.Extra (including the "no default asserted"
//   contract requirement for shutdown_grace) — all pure logic, no real
//   process/socket/signal, so this file has no build tag and runs
//   identically on every platform including the Windows CI lane
//   (R-14.131). The fail-loud subsystem Manifest (R-14.87) tests split out
//   to daemon_manifest_test.go under R-14.117/R-14.133 (Art.10.3's
//   300-line file cap).
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
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
			// actual StartTime BEFORE rec.StartedAt (a negative diff before
			// the abs()) — the recycled process happens to have started
			// earlier than the pidfile's recorded time, e.g. clock skew
			// between the write and the ps probe. Exercises classifyPID's
			// `if diff < 0 { diff = -diff }` branch, distinct from the
			// "recycled" case above where actual is AFTER rec.StartedAt.
			"alive, PID recycled: actual start time is BEFORE the recorded one",
			pidRecord{PID: 6, StartedAt: base}, true,
			fakeProber{alive: map[int]bool{6: true}, startTime: map[int]time.Time{6: base.Add(-time.Hour)}},
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
			name: "daemon section present but shutdown_grace key absent: falls back, grace unset",
			cfg: &runtime.Config{Extra: map[string]interface{}{
				"daemon": map[string]interface{}{"socket": "/custom.sock"},
			}},
			wantSocket: "/custom.sock",
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
