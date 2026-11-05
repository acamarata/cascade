//go:build !windows

// Purpose: writes the daemon-owned execution kernel file at daemon start, so
// every spawn reads the kernel from <DataDir>/policy/policy-kernel.md, a path
// outside every worktree that no repository can edit (P1-GEN-12).
// Inputs: the daemon wiring's paths (DataDir) and manifest.
// Outputs: the canonical kernel file (policy.EnsureDaemonKernelFile) and a
// "policy-kernel" manifest entry naming its path.
// Constraints: phaseCore, before any namespace or spawn path is wired. A
// failure (a symlinked policy directory, an unwritable data directory) stops
// the daemon, reported by runDaemonWiring under "policy-kernel"; the daemon
// never starts with a missing or non-canonical kernel.
// SPORT: cmd/cascade daemon registrations (policy-kernel, P1-GEN-12a).
package main

import (
	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// policyKernelSubsystem is the registration and manifest name.
const policyKernelSubsystem = "policy-kernel"

var _ = registerDaemonWiring(daemonRegistration{
	// After egress-firewall (15), which must follow status-get directly,
	// and before session-scope (20) and every namespace or spawn path.
	Name: policyKernelSubsystem, Phase: phaseCore, Order: 18,
	Wire: wirePolicyKernel,
})

// wirePolicyKernel writes the canonical kernel under w.Paths.DataDir() and
// records its path on the manifest.
func wirePolicyKernel(w *daemonWiring) error {
	if w.Paths == nil {
		return cascade.New(cascade.KindInternal, "policy kernel: the daemon wiring has no paths")
	}
	path, err := policy.EnsureDaemonKernelFile(w.Paths.DataDir())
	if err != nil {
		return err
	}
	if w.Manifest != nil {
		w.Manifest.Started(policyKernelSubsystem, path)
	}
	return nil
}
