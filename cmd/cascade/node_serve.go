// Purpose: `cascade node serve` — the node agent's CLI composition root:
//
//	wires internal/nodes' real identity/record/keystore/known_hosts/action
//	backends, mounts its RPC surface (node.enroll, node.heartbeat,
//	node.dispatch.execute) on the node's own unix socket, and serves it
//	until a termination signal.
//
// Inputs: process args/flags (none beyond the persistent globals); the
//
//	real environment (paths, OS keystore, wall clock, signals).
//
// Outputs: a running foreground process; nil on a clean drained shutdown,
//
//	a typed error on any setup failure.
//
// Constraints: 06-FORGE-SPEC §2 — Windows tier-2: this command refuses
//
//	unconditionally on Windows (internal/nodes.RefuseOnGOOS). Mounted on
//	the cobra root per R-16.56: THIS ticket mounts `node serve` only;
//	enroll/list/status/drain/remove are S-36.T4's mounts under the same
//	`node` group. This file is the promised caller_site for P1-E17-W4-S36-T2's
//	testonly-allow.json entries (GenerateIdentity, NewFileKnownHostsBackend,
//	NewFileRecordBackend, NewKnownHosts, NewNodeKeystore, NewRecordStore,
//	RegisterHandlers, Satisfies, SignRotationRequest, SignTranscript,
//	SignatureRevoked) and, as of the R-16.80-class wiring fix below, for
//	internal/nodes.NewProber/NewNetworkWatcher too.
//
// SPORT: cmd/cascade/node (ADD, per T-2 sport_updates).

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mountNodeCmd attaches the `node` command group with its `serve`
// subcommand, following mountDaemonCmd's exact pattern, plus (S-36.T4)
// every other node.* verb: enroll/list/status/drain/remove/rotate-key/
// revoke, mounted via node.go's mountNodeCLICmds.
func mountNodeCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage this machine's participation in the node fleet",
	}
	cmd.AddCommand(newNodeServeCmd())
	mountNodeCLICmds(cmd, productionNodeCLIDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newNodeServeCmd builds `cascade node serve`.
func newNodeServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the node agent (the same binary, in node-serve mode)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNodeServe(cmd.Context(), productionNodeServeDeps())
		},
	}
}

// nodeServeDeps carries every environment-touching input runNodeServe
// needs, injected so tests never resolve the real CASCADE_HOME or touch a
// real OS keychain (Art.7.1), mirroring daemonDeps/fakeDaemonPaths'
// established pattern in this package.
type nodeServeDeps struct {
	Paths cruntime.PathProvider
	Clock cruntime.Clock
	// SecretsDir, when set, forces the encrypted file-vault backend (via
	// secrets.Config.Dir + ForceFileVault) instead of a real OS keychain.
	// Empty in production: NewNodeKeystore's own selection order applies.
	SecretsDir string
	GOOS       string
	// ProbeTicker replaces the real Prober ticker startPresenceSubsystems
	// otherwise builds; nil in production. Tests inject a fake so a
	// probe pass runs deterministically without a real clock wait.
	ProbeTicker nodes.Ticker
}

// productionNodeServeDeps builds nodeServeDeps against the real
// environment.
func productionNodeServeDeps() nodeServeDeps {
	return nodeServeDeps{Paths: lazyPaths{}, Clock: cruntime.SystemClock{}, GOOS: runtime.GOOS}
}

// nodeServeComposition is runNodeServe's built collaborators, split out of
// runNodeServe itself so that function stays under the 50-line cap.
// recordStore/knownHosts are kept here (not only closed over inside
// registry) because node_serve_presence.go's Prober/NetworkWatcher need
// the SAME instances the RPC surface uses, never a second one.
type nodeServeComposition struct {
	dataDir     string
	keystore    *nodes.NodeKeystore
	self        nodes.Identity
	registry    *nodes.ServeRegistry
	recordStore *nodes.RecordStore
	knownHosts  *nodes.KnownHosts
}

// runNodeServe is the platform-independent entry point: it refuses on
// Windows (tier-2), then builds and serves the real registry.
func runNodeServe(ctx context.Context, deps nodeServeDeps) error {
	if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
		return err
	}
	comp, closeAttest, err := composeNodeServeAttested(ctx, deps)
	if err != nil {
		return drainOrFail(ctx, err)
	}
	defer closeAttest()
	stopBackground, err := startNodeServeBackgroundLoops(ctx, comp, deps)
	if err != nil {
		return drainOrFail(ctx, err)
	}
	defer stopBackground()

	socketPath := filepath.Join(comp.dataDir, "nodes", "node.sock")
	ln, err := listenNodeSocket(socketPath)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "node serve: listen on node socket")
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	srv := &http.Server{Handler: comp.registry.Handler(), ConnContext: nodes.ConnContext}
	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- srv.Serve(ln) }()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	select {
	case <-ctx.Done():
	case <-sigs:
	case err := <-serveErrCh:
		if err != nil && err != http.ErrServerClosed {
			return cascade.Wrap(cascade.KindUnavailable, err, "node serve: listener failed")
		}
	}
	_ = srv.Shutdown(context.Background())
	return nil
}

// composeNodeServeAttested is composeNodeServe with ci.attest.sign mounted:
// a NodeAttestor over the node's own store is composed into
// ServeDeps.AttestSigner, with the pin read from the node's own binding.
func composeNodeServeAttested(ctx context.Context, deps nodeServeDeps) (nodeServeComposition, func(), error) {
	comp, err := composeNodeServe(ctx, deps)
	if err != nil {
		return comp, nil, err
	}
	attestor, closeStore, err := ci.OpenNodeAttestor(ctx, comp.self, comp.keystore, comp.dataDir, deps.Clock)
	if err != nil {
		return comp, nil, err
	}
	putter, err := attestor.NewAttestPutter()
	if err != nil {
		_ = closeStore()
		return comp, nil, err
	}
	actionLog, enrollmentID := nodeDispatchLeg(comp.dataDir, comp.self)
	comp.registry = nodes.NewServeRegistry(nodes.ServeDeps{
		Records: comp.recordStore, KnownHosts: comp.knownHosts, Keystore: comp.keystore, Self: comp.self,
		Sequences: nodes.NewSequenceStore(), Clock: deps.Clock, Timeout: nodes.DefaultHeartbeatTimeout,
		Actions: actionLog, EnrollmentID: enrollmentID,
		AttestSigner: attestor.Sign, AttestPutter: putter.Put, ControllerBinding: nodes.NewFileControllerBindingBackend(comp.dataDir),
	})
	return comp, func() { _ = closeStore() }, nil
}

// startNodeServeBackgroundLoops starts every background loop runNodeServe
// keeps alive for its whole run: the outbound heartbeat (a silent no-op
// pre-enrollment) and the presence/network-change subsystems
// (node_serve_presence.go). Split out so runNodeServe itself stays under
// the 50-line cap, and so the two independently-cancelable contexts each
// loop needs are built and deferred in exactly one place.
func startNodeServeBackgroundLoops(ctx context.Context, comp nodeServeComposition, deps nodeServeDeps) (func(), error) {
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	startOutboundHeartbeat(heartbeatCtx, comp.dataDir, comp.self, comp.keystore, deps.Clock)

	presenceCtx, stopPresence := context.WithCancel(ctx)
	closePresence, err := startPresenceSubsystems(presenceCtx, comp, deps)
	if err != nil {
		stopHeartbeat()
		stopPresence()
		return nil, err
	}
	return func() {
		stopHeartbeat()
		stopPresence()
		closePresence()
	}, nil
}

// startOutboundHeartbeat launches the node-initiated periodic heartbeat
// (this ticket's own contract: "node-initiated periodic heartbeat to the
// controller endpoint captured at enrollment") when this node has a
// ControllerBinding on disk. No binding yet (a node that has never
// enrolled) is a valid, silent no-op — there is nothing to heartbeat to.
func startOutboundHeartbeat(ctx context.Context, dataDir string, self nodes.Identity, keystore *nodes.NodeKeystore, clocks ...cruntime.Clock) {
	binding, ok, err := nodes.NewFileControllerBindingBackend(dataDir).Load()
	if err != nil || !ok {
		return
	}
	var seq uint64
	var clock cruntime.Clock = cruntime.SystemClock{}
	if len(clocks) > 0 {
		clock = clocks[0]
	}
	go nodes.RunHeartbeatLoop(ctx, nodes.HeartbeatLoopOptions{
		Ticker:       nodes.NewSystemTicker(nodes.DefaultHeartbeatInterval),
		NextSequence: func() uint64 { seq++; return seq },
		Send:         httpHeartbeatSender(binding, self.NodeID, clock, dialHeartbeatSocket),
		BuildReport: func() nodes.CapabilityReport {
			return nodes.CapabilityReport{K12: nodes.NewK12Preset(0, runtime.NumCPU())}
		},
		Keystore:     keystore,
		NodeID:       self.NodeID,
		EnrollmentID: binding.EnrollmentID,
	})
}

// listenNodeSocket binds a unix socket at path with 0600 permissions,
// mirroring internal/daemon/lifecycle_unix.go's listenSocket precedent
// (that file belongs to a different package and a different ticket's
// files_scope, so this is a small, deliberate, documented duplication
// rather than a cross-ticket import of unexported daemon internals).
func listenNodeSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path) // best-effort: clear a stale socket from a prior crashed run
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
