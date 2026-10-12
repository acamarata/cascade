//go:build !windows

package main

// Purpose (this file): the [secrets] detector tests that need a fresh
//   process, and the daemon's egress-firewall registration. The process
//   detector takes the operator's [secrets] section from config.toml,
//   refusing an invalid one with the key named, and the daemon refuses to
//   start on it under the "egress-firewall" registration.
// Constraints: !windows because the daemon wiring is (the Windows daemon
//   refuses every verb). The detector tests run in a child copy of this
//   test binary, because the process detector is resolved once per process
//   and the parent's was resolved long before these tests run.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// detectorChildEnv selects the child half of the detector tests.
const detectorChildEnv = "EGRESSFW_DETECTOR_CHILD"

// runDetectorChild runs test in a fresh copy of this binary with
// CASCADE_HOME holding config, and returns its output and error.
func runDetectorChild(t *testing.T, test, config string) (string, error) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$", "-test.v", "-test.count=1") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), detectorChildEnv+"=1", "CASCADE_HOME="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// bearerSample is a credential the detector scores as corroborated (0.90):
// above the shipped 0.8 threshold, below an operator's 0.95.
const bearerSample = "Authorization: Bearer Zq8Rt2Lm5Np7Vx3Kc9Hw4Bd6"

// recordingVault is the scrub pipeline's vault: it records what would be
// stored and stores nothing.
type recordingVault struct{ names []string }

func (v *recordingVault) Set(_ context.Context, name string, _ []byte, _ secrets.SetMode) (secrets.SetResult, error) {
	v.names = append(v.names, name)
	return secrets.SetResult{}, nil
}

func TestProcessDetectorHonoursOperatorThreshold(t *testing.T) {
	if os.Getenv(detectorChildEnv) == "1" {
		detectorThresholdChild(t)
		return
	}
	out, err := runDetectorChild(t, "TestProcessDetectorHonoursOperatorThreshold",
		"[secrets]\nconfidence_threshold = 0.95\n")
	if err != nil || !strings.Contains(out, "--- PASS: TestProcessDetectorHonoursOperatorThreshold") {
		t.Fatalf("child run failed (%v):\n%s", err, out)
	}
}

// detectorThresholdChild is the child half: a fresh process whose first
// ProcessDetector call reads CASCADE_HOME/config.toml.
func detectorThresholdChild(t *testing.T) {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatalf("ProcessDetector: %v", err)
	}
	if got := detector.Config().ConfidenceThreshold; got != 0.95 {
		t.Fatalf("ConfidenceThreshold = %v, want the operator's 0.95", got)
	}
	var top secrets.Confidence
	for _, hit := range detector.Scan([]byte(bearerSample)) {
		if hit.Confidence > top {
			top = hit.Confidence
		}
	}
	if top < secrets.Confidence(secrets.DefaultConfidenceThreshold) || top >= 0.95 {
		t.Fatalf("sample scores %v; it must sit between the shipped and the operator threshold", top)
	}
	// The conversation scrub runs the same detector: at 0.95 the
	// corroborated sample is not certain, so nothing is quarantined or
	// rewritten. Under the shipped 0.8 it would be.
	vault := &recordingVault{}
	pipeline, err := conversation.NewDefaultScrubPipelineOverVault(t.TempDir(),
		runtime.NewFixedClock(time.Unix(0, 0)), vault, nil)
	if err != nil {
		t.Fatalf("scrub pipeline: %v", err)
	}
	out, err := pipeline.ScrubTurn(context.Background(), []conversation.ScrubSegment{{Ref: "s1", Content: []byte(bearerSample)}})
	if err != nil {
		t.Fatalf("ScrubTurn: %v", err)
	}
	if len(out) != 1 || !bytes.Equal(out[0], []byte(bearerSample)) || len(vault.names) != 0 {
		t.Fatalf("scrub acted on a hit below the operator's threshold: out=%q vaulted=%v", out, vault.names)
	}
}

func TestProcessDetectorRefusesInvalidSecretsSection(t *testing.T) {
	if os.Getenv(detectorChildEnv) == "1" {
		detectorInvalidChild(t)
		return
	}
	out, err := runDetectorChild(t, "TestProcessDetectorRefusesInvalidSecretsSection",
		"[secrets]\nconfidence_threshold = \"0.8\"\n")
	if err != nil || !strings.Contains(out, "--- PASS: TestProcessDetectorRefusesInvalidSecretsSection") {
		t.Fatalf("child run failed (%v):\n%s", err, out)
	}
}

// detectorInvalidChild is the child half: the invalid section is refused by
// ProcessDetector and by the daemon's egress-firewall registration.
func detectorInvalidChild(t *testing.T) {
	detector, err := secrets.ProcessDetector()
	if detector != nil || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "confidence_threshold") {
		t.Fatalf("ProcessDetector = (%v, %v), want a KindInvalidInput refusal naming confidence_threshold", detector, err)
	}
	scratchRegistrations(t)
	var mine []daemonRegistration
	for _, r := range daemonRegistrations {
		if r.Name == "egress-firewall" {
			mine = append(mine, r)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("found %d egress-firewall registrations, want exactly one", len(mine))
	}
	daemonRegistrations = mine
	err = runDaemonWiring(&daemonWiring{Paths: firewallTestPaths(t)})
	if err == nil || !strings.Contains(err.Error(), `"egress-firewall"`) || !strings.Contains(err.Error(), "confidence_threshold") {
		t.Fatalf("runDaemonWiring = %v, want a refusal naming egress-firewall and confidence_threshold", err)
	}
}

// TestEgressFirewallWiringRecordsTheManifest: the daemon's egress-firewall
// step builds the firewall over the daemon's paths and records the detector
// settings in force on the manifest status.get serves.
func TestEgressFirewallWiringRecordsTheManifest(t *testing.T) {
	resetProcessFirewall(t)
	manifest := daemon.NewManifest(nil, runtime.NewFixedClock(time.Unix(0, 0)))
	if err := wireEgressFirewall(&daemonWiring{Paths: firewallTestPaths(t), Manifest: manifest}); err != nil {
		t.Fatalf("wireEgressFirewall: %v", err)
	}
	cfg := secrets.ProcessDetectionConfig()
	want := fmt.Sprintf("confidence_threshold=%g entropy_floor=%g", cfg.ConfidenceThreshold, cfg.EntropyFloor)
	for _, s := range manifest.Snapshot() {
		if s.Name == egressFirewallSubsystem {
			if s.Detail != want {
				t.Fatalf("detail = %q, want %q", s.Detail, want)
			}
			return
		}
	}
	t.Fatal("the manifest has no egress-firewall entry")
}

// TestEgressFirewallWiringPosition pins where the firewall is built: in
// phaseCore, right after status-get, and before session-scope and every
// plugin, conductor or transport registration that may ask for an engine.
func TestEgressFirewallWiringPosition(t *testing.T) {
	regs := sortedDaemonRegistrations(daemonRegistrations)
	pos := map[string]int{}
	for i, r := range regs {
		pos[r.Name] = i
		if r.Name == egressFirewallSubsystem && r.Phase != phaseCore {
			t.Errorf("%s runs in phase %d, want phaseCore", r.Name, r.Phase)
		}
	}
	at, ok := pos[egressFirewallSubsystem]
	if !ok || pos["status-get"] >= at || pos["session-scope"] <= at {
		t.Fatalf("positions: status-get %d, %s %d (registered %v), session-scope %d",
			pos["status-get"], egressFirewallSubsystem, at, ok, pos["session-scope"])
	}
	for _, r := range regs[:at] {
		if r.Phase != phaseMiddleware && r.Name != "status-get" {
			t.Errorf("%q is wired before the egress firewall", r.Name)
		}
	}
}

// TestDaemonCredentialSourceBindsAsOwner: the daemon's credential reader
// pairs a grant register under its own data directory with the broker bound
// to that directory, even after an earlier caller bound another one.
func TestDaemonCredentialSourceBindsAsOwner(t *testing.T) {
	resetProcessFirewall(t)
	if _, err := processFirewallFor(firewallTestPaths(t)); err != nil {
		t.Fatal(err)
	}
	paths := firewallTestPaths(t)
	if _, err := daemonCredentialSource(paths); err != nil {
		t.Fatalf("daemonCredentialSource over a second directory = %v, want the owner's rebind", err)
	}
	if got := processFirewallState.dataDir; got != paths.DataDir() {
		t.Fatalf("firewall bound to %q, want the credential source's %q", got, paths.DataDir())
	}
}
