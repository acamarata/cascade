//go:build !windows

// Purpose: bootstrap the controller designation of a single-machine home at
//
//	daemon start, before any role consumer (the job scheduler, the job/lease
//	RPC guard, the envelope signer) resolves the role.
//
// Inputs: the daemon's data dir, its node keystore seam and clock.
// Outputs: an epoch-one "bootstrap" designation when the home has no
//
//	designation, no binding and no bound_once tombstone; nothing otherwise.
//
// Constraints: a failure is logged and never aborts start; the role
//
//	consumers then report the conflict themselves (fail closed).
//
// SPORT: cmd/cascade/daemon (CHANGED, nodes/controller-designation).
package main

import (
	"context"
	"crypto/rand"
	"log/slog"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// bootstrapDaemonDesignation runs nodes.BootstrapDesignation over the
// daemon's data dir. Without it a fresh home resolves to a role conflict and
// the scheduler never runs.
func bootstrapDaemonDesignation(ctx context.Context, deps daemonDeps, dataDir string, logger *slog.Logger) {
	d, wrote, err := ensureDaemonDesignation(ctx, deps, dataDir)
	switch {
	case err != nil:
		logger.Warn("daemon: controller designation not bootstrapped; controller-only verbs refuse", "error", err)
	case wrote:
		logger.Info("daemon: bootstrapped the controller designation", "installation", d.InstallationID, "epoch", d.Epoch)
	}
}

// ensureDaemonDesignation returns the stored designation, writes the
// bootstrap one, or returns the reason it may not. The local identity (and
// the keystore behind it) is only touched when a bootstrap may happen.
func ensureDaemonDesignation(ctx context.Context, deps daemonDeps, dataDir string) (nodes.Designation, bool, error) {
	if dataDir == "" {
		return nodes.Designation{}, false, cascade.New(cascade.KindUnavailable, "daemon: no data directory for the controller designation")
	}
	designation := nodes.NewFileDesignationBackend(dataDir)
	if stored, found, err := designation.Load(); err != nil || found {
		return stored, false, err
	}
	binding := nodes.NewFileControllerBindingBackend(dataDir)
	if _, bound, err := binding.Load(); err != nil || bound {
		// A bound node never bootstraps: its role comes from the binding.
		return nodes.Designation{}, false, err
	}
	self, err := daemonLocalIdentity(ctx, deps, dataDir)
	if err != nil {
		return nodes.Designation{}, false, err
	}
	return nodes.BootstrapDesignation(ctx, designation, binding, dataDir, self, rand.Reader, deps.Clock)
}

// daemonLocalIdentity is this machine's node identity, the one `node enroll`
// uses, generated on first use under the daemon's keystore.
func daemonLocalIdentity(ctx context.Context, deps daemonDeps, dataDir string) (nodes.Identity, error) {
	backend := nodes.NewFileSelfIdentityBackend(dataDir)
	if existing, ok, err := backend.Load(); err != nil {
		return nodes.Identity{}, err
	} else if ok {
		return nodes.ValidateIdentity(existing.NodeID, existing.PubKeyB64)
	}
	constructor := deps.NewKeystore
	if constructor == nil {
		constructor = nodes.NewNodeKeystore
	}
	keystore, err := constructor(nodeKeystoreConfig(deps.SecretsDir, dataDir))
	if err != nil {
		return nodes.Identity{}, err
	}
	return nodes.EnsureLocalIdentity(ctx, backend, keystore, rand.Reader)
}
