//go:build !windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// onlyPolicyKernelRegistration narrows daemonRegistrations to the one
// policy-kernel registration (restored on cleanup) and returns it.
func onlyPolicyKernelRegistration(t *testing.T) daemonRegistration {
	t.Helper()
	scratchRegistrations(t)
	var mine []daemonRegistration
	for _, r := range daemonRegistrations {
		if r.Name == "policy-kernel" {
			mine = append(mine, r)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("found %d policy-kernel registrations, want exactly one", len(mine))
	}
	daemonRegistrations = mine
	return mine[0]
}

func policyKernelTestWiring(paths runtime.PathProvider) *daemonWiring {
	return &daemonWiring{Registry: rpc.NewRegistry(), Paths: paths,
		Manifest: daemon.NewManifest(nil, runtime.NewFixedClock(time.Unix(0, 0)))}
}

// TestPolicyKernelWired: the composed daemon registers policy-kernel in
// phaseCore; running it writes the canonical 0600 kernel under DataDir and
// records it on the manifest, and a symlinked policy directory stops the
// wiring with an error naming "policy-kernel".
func TestPolicyKernelWired(t *testing.T) {
	reg := onlyPolicyKernelRegistration(t)
	if reg.Phase != phaseCore {
		t.Fatalf("policy-kernel phase = %d, want phaseCore (%d)", reg.Phase, phaseCore)
	}
	paths := firewallTestPaths(t)
	w := policyKernelTestWiring(paths)
	if err := runDaemonWiring(w); err != nil {
		t.Fatalf("runDaemonWiring: %v", err)
	}
	p := filepath.Join(paths.DataDir(), filepath.FromSlash(policy.KernelFileRel))
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("kernel file %s = %v, %v; want a regular 0600 file", p, info, err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != provider.PolicyKernelSHA256 {
		t.Fatalf("kernel sha256 = %x, want %s", sum, provider.PolicyKernelSHA256)
	}
	recorded := false
	for _, s := range w.Manifest.Snapshot() {
		recorded = recorded || (s.Name == "policy-kernel" && s.Detail == p)
	}
	if !recorded {
		t.Fatalf("manifest has no policy-kernel entry naming %s: %+v", p, w.Manifest.Snapshot())
	}

	hostile := firewallTestPaths(t)
	if err := os.MkdirAll(hostile.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(hostile.DataDir(), "policy")); err != nil {
		t.Fatal(err)
	}
	err = runDaemonWiring(policyKernelTestWiring(hostile))
	ce, ok := err.(*cascade.Error)
	if !ok {
		t.Fatalf("runDaemonWiring over a symlinked policy dir = %v (%T), want a *cascade.Error", err, err)
	}
	if ce.Kind != cascade.KindConflict || !strings.Contains(err.Error(), `"policy-kernel"`) ||
		!strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("error = {%s, %q}, want a KindConflict naming \"policy-kernel\" and the symlink", ce.Kind, err)
	}
}

// TestPolicyKernelWireRefusesNoPaths: a wiring without paths is an internal
// error, never a silent skip.
func TestPolicyKernelWireRefusesNoPaths(t *testing.T) {
	err := wirePolicyKernel(&daemonWiring{})
	ce, ok := err.(*cascade.Error)
	if !ok || ce.Kind != cascade.KindInternal || !strings.Contains(ce.Msg, "no paths") {
		t.Fatalf("wirePolicyKernel(no paths) = %v, want KindInternal naming no paths", err)
	}
}
