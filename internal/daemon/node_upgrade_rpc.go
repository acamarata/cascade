// Purpose: T0-daemon-composition-root — registers "node.upgrade" on the
//   daemon's RPC router over a real internal/nodes.RunUpgrade, closing
//   the same wiring gap R-14.223 recorded for fleet.journal_show/replay:
//   internal/nodes.RegisterUpgradeHandler (S-36.T5) had a test caller
//   only until this file. Mirrors journal_rpc.go's RegisterFleetJournalHandler
//   precedent exactly: the composition logic (reading the staged
//   artifact, building the ssh signing identity) lives in this package,
//   cmd/cascade/daemon_unix_run.go only calls it with what buildRPCServer
//   already has open.
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider,
//   and runtime.Clock.
// Outputs: "node.upgrade" bound to a real Provision pipeline reading a
//   FIXED, documented staging location under DataDir()/nodes/upgrade/
//   (artifact + artifact.minisig) — an operator (or a future release-
//   fetch ticket, out of this ticket's scope) stages the files there
//   before calling `node upgrade` over RPC. This is a deliberate,
//   documented limitation, not a stub: every byte Provision acts on is
//   read from a real file and minisign-verified for real; only the
//   STAGING mechanism (how the file got there) is out of scope.
// Constraints: the minisign public key path is resolved from
//   CASCADE_MINISIGN_PUBKEY (os.Getenv, matching daemon.go's own direct
//   os.ReadFile precedent in this package), matching the release train's own
//   env-var convention (.goreleaser.yaml/release.yml) and
//   cmd/cascade/node_upgrade.go's identical CLI-side fallback. A missing
//   staged artifact or public key refuses the RPC call outright (fail
//   closed) rather than silently registering a handler that always
//   errors — RegisterNodeUpgradeHandler itself never fails to register;
//   only an actual "node.upgrade" call against a missing stage fails.
// SPORT: internal/daemon (ADD, T0-daemon-composition-root, P1-E17-W4-S36-T5).

package daemon

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RegisterNodeUpgradeHandler mounts "node.upgrade" on registry. Elevation
// is enforced by internal/rpc's shared ElevationMiddleware consulting the
// canonical elevationTable (already lists node.upgrade, always:true) —
// this function does not re-implement that gate, matching every sibling
// registerXHandler in this package.
func RegisterNodeUpgradeHandler(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) {
	nodes.RegisterUpgradeHandler(registry, func(ctx context.Context) (nodes.UpgradeDeps, error) {
		return resolveNodeUpgradeDeps(ctx, paths, clock)
	})
}

// resolveNodeUpgradeDeps reads the staged artifact/signature/pubkey and
// builds the daemon's own ssh signing identity, mirroring
// cmd/cascade/node_upgrade.go's composeNodeUpgrade/composeUpgradeDialer
// construction (same collaborators, daemon-side composition root
// instead of a CLI flag set).
func resolveNodeUpgradeDeps(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (nodes.UpgradeDeps, error) {
	dataDir := paths.DataDir()
	stageDir := filepath.Join(dataDir, "nodes", "upgrade")
	data, err := os.ReadFile(filepath.Join(stageDir, "artifact"))
	if err != nil {
		return nodes.UpgradeDeps{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: node.upgrade: no staged artifact")
	}
	sig, err := os.ReadFile(filepath.Join(stageDir, "artifact.minisig"))
	if err != nil {
		return nodes.UpgradeDeps{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: node.upgrade: no staged artifact signature")
	}
	pubkeyPath := os.Getenv("CASCADE_MINISIGN_PUBKEY")
	if pubkeyPath == "" {
		return nodes.UpgradeDeps{}, cascade.New(cascade.KindUnavailable, "daemon: node.upgrade: CASCADE_MINISIGN_PUBKEY is not set")
	}
	pubBytes, err := os.ReadFile(pubkeyPath)
	if err != nil {
		return nodes.UpgradeDeps{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: node.upgrade: read CASCADE_MINISIGN_PUBKEY")
	}
	pub, err := nodes.ParseMinisignPublicKey(pubBytes)
	if err != nil {
		return nodes.UpgradeDeps{}, err
	}
	keystore, err := nodes.NewNodeKeystore(secrets.Config{})
	if err != nil {
		return nodes.UpgradeDeps{}, err
	}
	self, err := nodes.EnsureLocalIdentity(ctx, nodes.NewFileSelfIdentityBackend(dataDir), keystore, rand.Reader)
	if err != nil {
		return nodes.UpgradeDeps{}, err
	}
	pubID, err := nodes.ParsePublicKey(self.PubKeyB64())
	if err != nil {
		return nodes.UpgradeDeps{}, err
	}
	signer := nodes.NewKeystoreExecSigner(ctx, keystore, self.NodeID, pubID)
	knownHosts := nodes.NewKnownHosts(nodes.NewFileKnownHostsBackend(dataDir))
	verifyFor := func(target nodes.Target) nodes.HostKeyVerifier {
		host := target.User + "@" + target.Addr
		return func(fingerprint string) error { return knownHosts.Verify(host, fingerprint, "") }
	}
	store := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), clock)
	return nodes.UpgradeDeps{
		Store:     store,
		Artifact:  nodes.Artifact{Data: data, Signature: sig},
		Provision: nodes.ProvisionDeps{Dialer: nodes.NewSSHExecDialer(signer, 0), VerifyFor: verifyFor, PublicKey: pub, ControllerVersion: buildinfo.Version},
	}, nil
}
