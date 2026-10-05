//go:build !windows

package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/hooks/egress"
)

func TestRegisterEgressClassIdempotent(t *testing.T) {
	reg := egress.NewRegistry()
	adapter := egressRegistryAdapter{reg: reg}
	if err := registerEgressClass(adapter, []string{"github.com"}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := registerEgressClass(adapter, []string{"github.com"}); err != nil {
		t.Fatalf("second registration should be a no-op, got: %v", err)
	}
	cfg, ok := reg.Lookup(egress.EgressClass(EgressClassPluginProcess))
	if !ok {
		t.Fatal("expected the plugin-process class to be registered")
	}
	if !cfg.Enabled || cfg.Owner != pluginProcessEgressOwner {
		t.Fatalf("unexpected class config: %+v", cfg)
	}
}

func TestRegisterEgressClassRequiresRegistrar(t *testing.T) {
	if err := registerEgressClass(nil, nil); err == nil {
		t.Fatal("expected an error for a nil Registrar")
	}
}

// TestExecAllowlistCoversProcessPackageOnly asserts the egress arch
// gate's allowlist state this ticket's contract depends on: process
// spawn is not egress (R-21.265), so internal/plugins/process must
// appear in the os/exec importer allowlist and must NOT appear in the
// net/net/http egress importer lists.
func TestExecAllowlistCoversProcessPackageOnly(t *testing.T) {
	execDirs := []string{}
	for _, e := range build.EgressExecNormative {
		execDirs = append(execDirs, e.Dir)
	}
	if !contains(execDirs, "internal/plugins/process") {
		t.Fatalf("internal/plugins/process missing from EgressExecNormative: %v", execDirs)
	}
	for _, e := range build.EgressNetNormative {
		if e.Dir == "internal/plugins/process" {
			t.Fatalf("internal/plugins/process must not appear in EgressNetNormative (process spawn is not egress, R-21.265)")
		}
	}
	for _, e := range build.EgressNetNotYetMigrated {
		if e.Dir == "internal/plugins/process" {
			t.Fatalf("internal/plugins/process must not appear in EgressNetNotYetMigrated")
		}
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestProcessRuntime_RealCounterpart drives Launch against a REAL forked
// OS process (/bin/sh, not this repo's code) that reads one line and
// replies with the exact bytes recorded in testdata/fixtures/
// handshake_ack.json (Art.2 real-counterpart; see
// testdata/fixtures/README.md for capture provenance). It is named for
// the ticket's `-tags integration go test -run TestProcessRuntime_
// RealCounterpart` check; it carries no integration-only import (no
// net), so it also runs in the default lane.
func TestProcessRuntime_RealCounterpart(t *testing.T) {
	script := `read _line; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocol_version":"1.0.0","manifest_hash":"deadbeef"}}'`
	rt := NewProcessRuntime()
	rt.Stderr = &bytes.Buffer{}
	rt.Registrar = egressRegistryAdapter{reg: egress.NewRegistry()}

	m := Manifest{Name: "sh-real-counterpart", TrustTier: TrustTierTrusted, Command: "sh", Args: []string{"-c", script}}
	h, err := rt.Launch(context.Background(), m)
	if err != nil {
		t.Skipf("real sh subprocess unavailable, skipping real-counterpart check: %v", err)
	}
	if h.Ack.ProtocolVersion != "1.0.0" || h.Ack.ManifestHash != "deadbeef" {
		t.Fatalf("unexpected ack from real subprocess: %+v", h.Ack)
	}
}

func TestFailedWaiterWait(t *testing.T) {
	if err := (failedWaiter{}).Wait(); err == nil {
		t.Fatal("expected an error from a failedWaiter")
	}
}

func TestResolvedStartupTimeoutExplicit(t *testing.T) {
	rt := &ProcessRuntime{StartupTimeout: 5 * time.Second}
	if got := rt.resolvedStartupTimeout(); got != 5*time.Second {
		t.Fatalf("resolvedStartupTimeout() = %v, want 5s", got)
	}
}

func TestLaunchRejectsMissingStderr(t *testing.T) {
	rt := &ProcessRuntime{Registrar: egressRegistryAdapter{reg: egress.NewRegistry()}}
	_, err := rt.Launch(context.Background(), trustedManifest("no-stderr"))
	if err == nil {
		t.Fatal("expected an error for a missing Stderr writer")
	}
}

// TestLaunchWiresEgressRegistration is the production-caller proof for
// the plugin-process egress class registration: it drives Launch (the
// real entry point), not registerEgressClass directly, and asserts the
// class landed in the H/S-16.T1 inventory as a SIDE EFFECT of calling
// Launch. See the ticket journal for the paired mutation test that
// removes Launch's call to registerEgressClassOnce and shows this test
// then fails.
func TestLaunchWiresEgressRegistration(t *testing.T) {
	reg := egress.NewRegistry()
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: reg},
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			return newFakeCommander("1.0.0", 0, false, nil)
		},
	}
	if _, err := rt.Launch(context.Background(), trustedManifest("wiring-proof")); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	cfg, ok := reg.Lookup(egress.EgressClass(EgressClassPluginProcess))
	if !ok {
		t.Fatal("Launch did not register the plugin-process egress class")
	}
	if cfg.Owner != pluginProcessEgressOwner {
		t.Fatalf("unexpected registered owner: %+v", cfg)
	}
}

func TestRespawnFailurePathAdvancesMonitorLoop(t *testing.T) {
	audit := &fakeAuditSink{}
	attempt := 0
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		Restart:   RestartPolicy{MaxAttempts: 1, InitialBackoff: time.Millisecond},
		Audit:     audit,
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			attempt++
			if attempt == 1 {
				// The initial Launch succeeds and crashes immediately.
				return newFakeCommander("1.0.0", 9, true, nil)
			}
			// Every respawn attempt fails outright: StdinPipe errors,
			// so spawnOne (and therefore respawn) returns an error and
			// the monitor loop falls back to failedWaiter.
			return failingCommander{}
		},
	}
	h, err := rt.Launch(context.Background(), trustedManifest("respawn-fails"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.State() != "invalid" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.State() != "invalid" {
		t.Fatalf("expected state-invalid, got %q", h.State())
	}
}

// failingCommander fails at StdinPipe, so spawnOne never reaches Start.
type failingCommander struct{}

func (failingCommander) StdinPipe() (io.WriteCloser, error) {
	return nil, errors.New("process: fake stdin pipe failure")
}
func (failingCommander) StdoutPipe() (io.ReadCloser, error) { return nil, nil }
func (failingCommander) Start() error                       { return nil }
func (failingCommander) Wait() error                        { return nil }
func (failingCommander) Signal(os.Signal) error             { return nil }

// TestLaunchChildGetsOnlyManifestEnv: the parent holds a principal token,
// a provider key and a canary; the real child (and its respawn) sees
// exactly Manifest.Env and nothing else, and an empty Env is an empty
// environment. A factory that leaves cmd.Env nil inherits all three.
func TestLaunchChildGetsOnlyManifestEnv(t *testing.T) {
	bin := buildLifecyclePlugin(t)
	secrets := map[string]string{
		"CASCADE_PRINCIPAL_TOKEN": "cpt-" + "canary-0001",
		"ANTHROPIC_API_KEY":       "sk-" + "ant-" + "canary-0002",
		"PLG09_PARENT_CANARY":     "parent-" + "only-0003",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	for _, tc := range []struct {
		name string
		env  []string
	}{{"listed", []string{"ALPHA=1", "BETA=two words"}}, {"empty", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rt := newRealRuntime()
			rt.Restart = RestartPolicy{MaxAttempts: 1, InitialBackoff: time.Millisecond}
			launchReal(context.Background(), t, rt, bin, tc.env, "-dir", dir, "-crash-first", filepath.Join(dir, "crashed"))
			for _, got := range readEnvFiles(t, dir, 2) { // the first child and its respawn
				want := slices.Sorted(slices.Values(tc.env))
				if !slices.Equal(got, want) {
					// Names only: on a regression the child holds the test
					// runner's real environment, and its values (CI tokens
					// included) must never reach a log.
					t.Fatalf("child environment names = %q, want exactly Manifest.Env's %q", envNames(got), envNames(want))
				}
				for _, v := range secrets {
					if strings.Contains(strings.Join(got, "\n"), v) {
						t.Fatalf("parent secret %q reached the child", v)
					}
				}
			}
		})
	}
}

// envNames returns the KEY part of each KEY=VALUE entry.
func envNames(entries []string) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i], _, _ = strings.Cut(e, "=")
	}
	return out
}

// readEnvFiles waits for n env-<pid> files in dir and returns each one's
// sorted entries.
func readEnvFiles(t *testing.T, dir string, n int) [][]string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		paths, _ := filepath.Glob(filepath.Join(dir, "env-*"))
		if len(paths) >= n {
			var out [][]string
			for _, p := range paths {
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("read %s: %v", p, err)
				}
				var entries []string
				if len(raw) > 0 {
					entries = strings.Split(string(raw), "\n")
				}
				out = append(out, slices.Sorted(slices.Values(entries)))
			}
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("found %d env files in %s, want %d (did the respawn happen?)", len(paths), dir, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHandleAlive: true while the real child runs, false once it exits on
// its own (stdin EOF, no respawn), and false after Close.
func TestHandleAlive(t *testing.T) {
	bin := buildLifecyclePlugin(t)
	exits := launchReal(context.Background(), t, newRealRuntime(), bin, nil)
	if !exits.Alive() {
		t.Fatal("Alive() = false right after Launch")
	}
	exits.mu.RLock()
	tr := exits.transport
	exits.mu.RUnlock()
	_ = tr.Close() // stdin EOF: the plugin exits by itself
	awaitTrue(t, "Alive() to turn false after the child exited", func() bool { return !exits.Alive() })

	closing := launchReal(context.Background(), t, newRealRuntime(), bin, nil)
	if !closing.Alive() {
		t.Fatal("Alive() = false right after Launch (second handle)")
	}
	if err := closing.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closing.Alive() {
		t.Fatal("Alive() = true after Close")
	}
}
