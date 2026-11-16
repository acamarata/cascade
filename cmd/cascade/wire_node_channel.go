//go:build !windows

// Purpose: the controller daemon's node channel. On the controller it builds
//
//	the node-facing registry over the daemon's device records, one dispatch
//	rendezvous and the dispatch journal, then a NodeChannel over the daemon's
//	tunnel Manager. NodeChannel.Reconcile starts one tunnel per enrolled,
//	routed node (and stops revoked, removed and finished drained ones); each
//	tunnel's far end is NodeFacing.DialAs(node id), never the owner socket.
//
// Inputs: the run context, w.Deps.Daemon.NodeTunnels, the data directory's
//
//	role records, device records and known hosts, and the installation key.
//
// Outputs: w.Deps.NodeFacing, w.Deps.Rendezvous and the NodeChannel stored
//
//	in w.Deps.NodeChannel; a Skipped manifest entry on any other role.
//
// Constraints: the installation signer (productionEnvelopeSigner) is the one
//
//	channel key: ssh userauth, the hello and NodeFacingDeps.Signer. The
//	controller NodeKeystore never signs here. No node-originated verb is
//	mounted on the owner registry, and no socket is opened for the far end.
package main

import (
	"context"
	"crypto/ed25519"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
)

const nodeChannelSubsystem = "node-channel"

var _ = registerDaemonWiring(daemonRegistration{
	Name: nodeChannelSubsystem, Phase: phaseFleet, Order: 30, Wire: wireNodeChannel,
})

// nodeChannelSeams are the two collaborators a test replaces: the ssh dialer
// built over the channel signer, and the reconcile ticker (nil: the system
// ticker).
type nodeChannelSeams struct {
	newDialer func(s scopeenvelope.Signer, controllerNodeID string) nodes.Dialer
	newTicker func(time.Duration) nodes.Ticker
}

func wireNodeChannel(w *daemonWiring) error {
	return wireNodeChannelWith(w, nodeChannelSeams{
		newDialer: func(s scopeenvelope.Signer, controllerNodeID string) nodes.Dialer {
			return nodes.NewSSHDialerFromSigner(s, controllerNodeID, 0)
		},
	})
}

// wireNodeChannelWith composes the channel on the controller and records
// Skipped with the reason on any other role; it never fails the daemon.
func wireNodeChannelWith(w *daemonWiring, seams nodeChannelSeams) error {
	if w.Deps.NodeChannel == nil {
		w.Deps.NodeChannel = new(atomic.Pointer[nodes.NodeChannel])
	}
	controllerNodeID, reason := nodeChannelController(w)
	if reason != "" {
		w.Manifest.Skipped(nodeChannelSubsystem, reason)
		return nil
	}
	dataDir := w.Paths.DataDir()
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), w.Clock)
	signer := installationChannelSigner{ctx: w.Ctx, paths: w.Paths}
	rv := nodes.NewRendezvous()
	facing := nodes.NewNodeFacing(nodes.NodeFacingDeps{
		Records: records, Sequences: nodes.NewSequenceStore(), Rendezvous: rv,
		Clock: w.Clock, Timeout: nodes.DefaultHeartbeatTimeout, Signer: signer,
	})
	daemon.RegisterNodeDispatchJournal(facing.Registry(), nodes.NewDispatcher(), journal.New(w.Store, w.Clock, "nodes.dispatch"))
	channel := nodes.NewNodeChannel(w.Deps.Daemon.NodeTunnels, records)
	channel.SetReconcileDeps(nodes.ReconcileDeps{
		Configure: nodeTunnelConfig(facing, seams.newDialer(signer, controllerNodeID),
			nodes.NewKnownHosts(nodes.NewFileKnownHostsBackend(dataDir)), w.Clock),
		NewTicker:  seams.newTicker,
		Rendezvous: rv,
	})
	w.Deps.NodeFacing, w.Deps.Rendezvous = facing, rv
	w.Deps.NodeChannel.Store(channel)
	w.Manifest.GoSupervised(w.Ctx, nodeChannelSubsystem, nodeChannelDetail(records), func(ctx context.Context) error {
		channel.Reconcile(ctx)
		return nil
	})
	return nil
}

// nodeChannelController returns the controller node id, or the reason the
// channel does not start: no data directory, no tunnel registry, a role that
// does not resolve, or a role other than controller.
func nodeChannelController(w *daemonWiring) (string, string) {
	if w.Paths == nil || w.Paths.DataDir() == "" {
		return "", "no data directory: the role cannot be resolved"
	}
	if w.Deps.Daemon.NodeTunnels == nil {
		return "", "no tunnel registry in this composition"
	}
	dataDir := w.Paths.DataDir()
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(dataDir))
	if err != nil {
		return "", "role does not resolve: " + err.Error()
	}
	if role != nodes.RoleController {
		return "", "role " + string(role) + ": node tunnels start only on the controller"
	}
	d, _, err := nodes.NewFileDesignationBackend(dataDir).Load()
	if err != nil {
		return "", "controller designation unreadable: " + err.Error()
	}
	return d.ControllerNodeID, ""
}

// nodeTunnelConfig is the Configure every reconciled tunnel takes: the
// installation-key dialer, the pinned known hosts with the enrolled host-key
// fingerprint as the explicit match, and the node-facing far end for this
// node. Reconcile fills Target and the route's controller.sock.
func nodeTunnelConfig(facing *nodes.NodeFacing, dialer nodes.Dialer, known *nodes.KnownHosts, clock runtime.Clock) func(nodes.Target, nodes.Route) nodes.TunnelConfig {
	return func(target nodes.Target, route nodes.Route) nodes.TunnelConfig {
		return nodes.TunnelConfig{
			Dialer: dialer, KnownHosts: known, HostKeyOverride: route.HostKeyFP,
			Clock: clock, LocalDial: facing.DialAs(target.NodeID),
		}
	}
}

// nodeChannelDetail is the manifest line: it names every enrolled node that
// has no complete route, which gets no tunnel until it is re-enrolled.
func nodeChannelDetail(records *nodes.RecordStore) string {
	const detail = "reconciling node tunnels"
	recs, err := records.List()
	if err != nil {
		return detail + "; device records unreadable: " + err.Error()
	}
	var routeless []string
	for _, rec := range recs {
		if _, _, err := records.Target(rec.NodeID); err != nil && !rec.Drained {
			routeless = append(routeless, rec.NodeID)
		}
	}
	if len(routeless) == 0 {
		return detail
	}
	sort.Strings(routeless)
	return detail + "; no complete route (no tunnel): " + strings.Join(routeless, ", ")
}

// installationChannelSigner is the installation key resolved per use through
// productionEnvelopeSigner, so its per-call controller check runs on every
// dial and hello and nothing is provisioned until a channel needs it.
type installationChannelSigner struct {
	ctx   context.Context
	paths runtime.PathProvider
}

// PublicKey is the installation public key, or nil when the key cannot be
// read; the dialer then refuses the dial.
func (s installationChannelSigner) PublicKey() ed25519.PublicKey {
	pub, err := installationPublicKey(s.ctx, s.paths)
	if err != nil {
		return nil
	}
	return pub
}

// Sign signs msg with the installation key.
func (s installationChannelSigner) Sign(ctx context.Context, msg []byte) ([]byte, error) {
	signer, err := productionEnvelopeSigner(ctx, s.paths)
	if err != nil {
		return nil, err
	}
	return signer.Sign(ctx, msg)
}
