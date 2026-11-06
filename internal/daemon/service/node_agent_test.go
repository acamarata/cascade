package service

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func nodeAgentFixture(t *testing.T, linger bool) (NodeAgentInstaller, Config, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	inst := NodeAgentInstaller{GOOS: "linux", LingerDir: t.TempDir()}
	cfg := Config{HomeDir: t.TempDir(), Executable: "/opt/cascade", User: "ci", Runner: runner}
	if linger {
		if err := os.WriteFile(filepath.Join(inst.LingerDir, cfg.User), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return inst, cfg, runner
}

func assertNodeError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	if !cascade.HasKind(err, kind) || err.Error() != kind.String()+": "+message {
		t.Fatalf("error = %v, want kind %v and message %q", err, kind, message)
	}
}

func assertNodeUntouched(t *testing.T, cfg Config, runner *fakeRunner) {
	t.Helper()
	entries, err := os.ReadDir(cfg.HomeDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("home entries = %v, error = %v; want empty home", entries, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unexpected runner calls: %v", runner.calls)
	}
}

func TestNodeAgentInstallRefusesWithoutLinger(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, false)
	_, err := inst.Install(cfg)
	assertNodeError(t, err, cascade.KindUnavailable, `node service: user "ci" has no systemd linger, so the node agent stops at logout and does not start at boot; run: sudo loginctl enable-linger ci`)
	assertNodeUntouched(t, cfg, runner)
}

func TestNodeAgentInstallWithLinger(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, true)
	cfg.NodeServeArgs = []string{"--capability", "gpu"}
	report, err := inst.Install(cfg)
	if err != nil || report.Action != ActionInstalled {
		t.Fatalf("Install = %+v, %v", report, err)
	}
	want := "# Managed by cascade node service install\n[Unit]\nDescription=Cascade node agent\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nExecStart=/opt/cascade node serve --capability gpu\nType=simple\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n"
	got, err := os.ReadFile(NodeAgentUnitPath(cfg.HomeDir))
	if err != nil || string(got) != want {
		t.Fatalf("unit = %q, %v; want %q", got, err, want)
	}
	wantCalls := []string{"systemctl --user daemon-reload", "systemctl --user enable cascade-node", "systemctl --user start cascade-node"}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", runner.calls, wantCalls)
	}
}

func TestNodeAgentLingerUnreadable(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, false)
	inst.LingerDir = filepath.Join(inst.LingerDir, "file")
	if err := os.WriteFile(inst.LingerDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := inst.Install(cfg)
	assertNodeError(t, err, cascade.KindUnavailable, "service: cannot read linger state for user ci")
	assertNodeUntouched(t, cfg, runner)
}

func TestNodeAgentUnsupportedGOOS(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, false)
	inst.GOOS = "darwin"
	_, err := inst.Install(cfg)
	assertNodeError(t, err, cascade.KindUnsupported, "node service: install is linux-only in P1")
	assertNodeUntouched(t, cfg, runner)
}

func TestNodeAgentForeignUnit(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, true)
	path := NodeAgentUnitPath(cfg.HomeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const foreign = "[Unit]\nDescription=operator unit\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := inst.Install(cfg)
	assertNodeError(t, err, cascade.KindConflict, "refusing to overwrite existing service unit not managed by cascade: "+path)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != foreign || len(runner.calls) != 0 {
		t.Fatalf("foreign unit = %q, error = %v, calls = %v", got, err, runner.calls)
	}
}

func TestNodeAgentUninstall(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, true)
	if _, err := inst.Install(cfg); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	report, err := inst.Uninstall(cfg)
	if err != nil || report.Action != ActionRemoved {
		t.Fatalf("Uninstall = %+v, %v", report, err)
	}
	if _, err := os.Stat(NodeAgentUnitPath(cfg.HomeDir)); !os.IsNotExist(err) {
		t.Fatalf("unit still present: %v", err)
	}
	want := []string{"systemctl --user stop cascade-node", "systemctl --user disable cascade-node", "systemctl --user daemon-reload"}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %v, want %v", runner.calls, want)
	}
}

func TestNewNodeAgentInstallerDefaults(t *testing.T) {
	inst := NewNodeAgentInstaller()
	if inst.GOOS != runtime.GOOS || inst.LingerDir != DefaultLingerDir {
		t.Fatalf("NewNodeAgentInstaller = %+v, want GOOS %q and LingerDir %q", inst, runtime.GOOS, DefaultLingerDir)
	}
}

func TestNodeAgentUninstallUnsupportedGOOS(t *testing.T) {
	inst, cfg, runner := nodeAgentFixture(t, false)
	inst.GOOS = "darwin"
	_, err := inst.Uninstall(cfg)
	assertNodeError(t, err, cascade.KindUnsupported, "node service: uninstall is linux-only in P1")
	assertNodeUntouched(t, cfg, runner)
}
