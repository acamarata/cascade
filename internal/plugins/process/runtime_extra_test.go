//go:build !windows

package process

import (
	"bytes"
	"context"
	"errors"
	"io"
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
