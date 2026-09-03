// Purpose: cobra-wiring tests for `cascade daemon install`/`uninstall`
//
//	(D/S-07.T2) — proves newDaemonCmd's install/uninstall RunE closures
//	assemble a service.Config from daemonDeps correctly and render the
//	Installer's DeltaReport, WITHOUT ever touching a real service manager
//	or the real home directory. This file deliberately builds the command
//	tree via newDaemonCmd(deps) directly rather than execDaemon/
//	newRootCmd(): newRootCmd wires productionDaemonDeps(), whose
//	HomeDir/Installer default to the REAL os.UserHomeDir()/
//	service.NewInstaller() — routing through it here would risk writing a
//	real launchd plist to this developer's actual
//	~/Library/LaunchAgents, exactly what this ticket's brief forbids. A
//	fakeServiceInstaller (this file) stands in for internal/daemon/
//	service's real Installer, so no test in this file ever execs
//	launchctl/systemctl or writes outside t.TempDir() (Art.7.1).
//
//	files_scope note: this ticket's contract lists only cmd/cascade/
//	daemon.go under files_scope.change; it names no cmd/cascade test file
//	even though its own checks list requires `go test ./cmd/cascade/...
//	-run TestDaemonService` to pass, and a cobra RunE closure cannot be
//	exercised without a _test.go file. This joins this ticket's authorized
//	write set on the same footing as R-14.117/R-14.133 (a file the
//	ticket's own required checks force into existence).
//
// SPORT: cmd/cascade/daemon (CHANGE, per D/S-07.T2 sport_updates).
package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/runtime"
)

// fakeServicePaths is a minimal runtime.PathProvider rooted at a
// t.TempDir(), local to this file (daemon_unix_test.go's fakeDaemonPaths
// is build-tagged !windows and this file is deliberately untagged so its
// tests run on every native CI runner, windows included, per this repo's
// ci.yml — the Installer these tests inject is always a fake regardless
// of platform, so there is nothing windows-specific to skip).
type fakeServicePaths struct{ root string }

func (p fakeServicePaths) Root() string       { return p.root }
func (p fakeServicePaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakeServicePaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakeServicePaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p fakeServicePaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p fakeServicePaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}

// fakeServiceInstaller records every Install/Uninstall call and returns
// pre-configured results — it never touches a filesystem or execs
// anything.
type fakeServiceInstaller struct {
	installCfg   service.Config
	installCalls int
	installOut   service.DeltaReport
	installErr   error

	uninstallCfg   service.Config
	uninstallCalls int
	uninstallOut   service.DeltaReport
	uninstallErr   error
}

func (f *fakeServiceInstaller) Install(cfg service.Config) (service.DeltaReport, error) {
	f.installCfg = cfg
	f.installCalls++
	return f.installOut, f.installErr
}

func (f *fakeServiceInstaller) Uninstall(cfg service.Config) (service.DeltaReport, error) {
	f.uninstallCfg = cfg
	f.uninstallCalls++
	return f.uninstallOut, f.uninstallErr
}

// newTestServiceDeps builds a daemonDeps for the install/uninstall tests:
// every environment touchpoint (Paths, HomeDir, Getuid, Executable) is a
// fixed fake, and Installer is the recording fake above.
func newTestServiceDeps(t *testing.T, inst *fakeServiceInstaller) daemonDeps {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	return daemonDeps{
		Paths:      fakeServicePaths{root: root},
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Executable: func() (string, error) { return "/usr/local/bin/cascade", nil },
		HomeDir:    func() (string, error) { return home, nil },
		Getuid:     func() int { return 4242 },
		Installer:  inst,
	}
}

// execDaemonService runs newDaemonCmd(deps) (NOT the full root command
// tree — see this file's package doc) with args and returns stdout.
func execDaemonService(t *testing.T, deps daemonDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newDaemonCmd(deps)
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.PersistentFlags().Bool("quiet", false, "")
	cmd.PersistentFlags().Bool("verbose", false, "")
	cmd.PersistentFlags().Bool("no-color", false, "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestDaemonService_InstallWiring proves `daemon install --json` builds
// the service.Config from daemonDeps (home dir, uid, executable, log path
// all sourced from the injected fakes — never a real environment call)
// and renders the fake Installer's DeltaReport through the standard --json
// envelope.
func TestDaemonService_InstallWiring(t *testing.T) {
	inst := &fakeServiceInstaller{installOut: service.DeltaReport{Action: service.ActionInstalled, Detail: "test install"}}
	deps := newTestServiceDeps(t, inst)

	out, err := execDaemonService(t, deps, "install", "--json")
	if err != nil {
		t.Fatalf("daemon install: %v (out=%s)", err, out)
	}
	if inst.installCalls != 1 {
		t.Fatalf("Install called %d times, want 1", inst.installCalls)
	}
	wantHome, _ := deps.HomeDir()
	if inst.installCfg.HomeDir != wantHome {
		t.Errorf("Config.HomeDir = %q, want %q", inst.installCfg.HomeDir, wantHome)
	}
	if inst.installCfg.Executable != "/usr/local/bin/cascade" {
		t.Errorf("Config.Executable = %q, want /usr/local/bin/cascade", inst.installCfg.Executable)
	}
	if inst.installCfg.UID != 4242 {
		t.Errorf("Config.UID = %d, want 4242", inst.installCfg.UID)
	}
	if inst.installCfg.Runner == nil {
		t.Error("Config.Runner is nil, want service.ExecRunner{}")
	}

	var envelope struct {
		Data struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode --json output: %v (out=%s)", err, out)
	}
	if envelope.Data.Action != service.ActionInstalled {
		t.Errorf("envelope action = %q, want %q", envelope.Data.Action, service.ActionInstalled)
	}
}

// TestDaemonService_UninstallWiring is install's counterpart.
func TestDaemonService_UninstallWiring(t *testing.T) {
	inst := &fakeServiceInstaller{uninstallOut: service.DeltaReport{Action: service.ActionNotInstalled, Detail: "test uninstall"}}
	deps := newTestServiceDeps(t, inst)

	out, err := execDaemonService(t, deps, "uninstall", "--json")
	if err != nil {
		t.Fatalf("daemon uninstall: %v (out=%s)", err, out)
	}
	if inst.uninstallCalls != 1 {
		t.Fatalf("Uninstall called %d times, want 1", inst.uninstallCalls)
	}

	var envelope struct {
		Data struct {
			Action string `json:"action"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode --json output: %v (out=%s)", err, out)
	}
	if envelope.Data.Action != service.ActionNotInstalled {
		t.Errorf("envelope action = %q, want %q", envelope.Data.Action, service.ActionNotInstalled)
	}
}

// TestDaemonService_InstallErrorPropagates proves an Installer failure
// surfaces as a command error rather than a success envelope (Art.1).
func TestDaemonService_InstallErrorPropagates(t *testing.T) {
	inst := &fakeServiceInstaller{installErr: errDaemonServiceTestSentinel}
	deps := newTestServiceDeps(t, inst)

	if _, err := execDaemonService(t, deps, "install"); err == nil {
		t.Fatal("want an error when the Installer fails, got nil")
	}
}

// TestDaemonService_InstallHelp proves install/uninstall are mounted and
// documented, matching the daemon command tree's other verbs.
func TestDaemonService_InstallHelp(t *testing.T) {
	out, err := execDaemonService(t, newTestServiceDeps(t, &fakeServiceInstaller{}), "install", "--help")
	if err != nil {
		t.Fatalf("daemon install --help: %v", err)
	}
	if !bytes.Contains([]byte(out), []byte("Install the cascade daemon")) {
		t.Errorf("help output = %q, want it to describe install", out)
	}
}

// TestDaemonService_ExtraArgsRejected proves install/uninstall reject
// positional arguments, matching every other daemon verb (root.go's
// guardUnknownSubcommands/cobra.NoArgs discipline).
func TestDaemonService_ExtraArgsRejected(t *testing.T) {
	deps := newTestServiceDeps(t, &fakeServiceInstaller{})
	if _, err := execDaemonService(t, deps, "install", "extra-arg"); err == nil {
		t.Error("want an error for an unexpected positional argument to install")
	}
	if _, err := execDaemonService(t, deps, "uninstall", "extra-arg"); err == nil {
		t.Error("want an error for an unexpected positional argument to uninstall")
	}
}

// errDaemonServiceTestSentinel is a fixed error value the error-
// propagation test injects.
var errDaemonServiceTestSentinel = &sentinelErr{"fake installer failure"}

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }
