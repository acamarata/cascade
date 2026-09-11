// Purpose: the travel profile (08-INIT-CONFIG-SPEC.md §3 Round-16,
//	R-16.37 §Nodes, R-21.198): the per-device ssh/VPN route T2's presence
//	prober falls back to once a direct probe has already failed, and the
//	travel⇒advertise-off precedence that gates T1's still-unbuilt mDNS
//	advertiser.
// Inputs: a DeviceRecord's optional Route (records.go), the existing
//	S-36.T3 tunnel Dialer/HostKeyVerifier, and this node's [nodes]
//	Section (config.go).
// Outputs: a real RouteChecker (prober.go's injectable seam) plus the
//	pure travel⇒advertise/browse decision functions T1's forward
//	discovery.go consumes.
// Constraints: NO NEW TRANSPORT (task 3) — RouteReachable dials through
//	the exact same Dialer interface tunnel.go's production sshDialer
//	implements, never a second ssh client. RouteConfig carries no
//	credential field: the private key never leaves NodeKeystore custody
//	(tunnel.go's keystoreSigner is the only signer), so there is nothing
//	here for a secret scanner to ever need to flag. "Cannot reach via
//	route" (dial attempted, failed) and "route not configured" (nothing
//	to dial) are deliberately DIFFERENT answers from RouteReachable
//	(distinct return shapes, both tested) even though the RouteChecker
//	interface built by S-72.T2 collapses both to false — that collapse
//	is T2's contract, not this file's; the finer distinction is real and
//	tested at RouteReachable's own level.
// SPORT: internal/nodes RouteConfig/ADDED, RouteReachable/ADDED,
//	NewSSHRouteChecker/ADDED (exported P1-E36-W7-S72 wiring fix,
//	was newSSHRouteChecker), advertisementAllowed/ADDED,
//	browseAllowed/ADDED (P1-E36-W7-S72-T3).

package nodes

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RouteConfig is the ssh/VPN route a device record's presence prober
// falls back to once its direct probe has failed. It reuses S-36.T1's
// <user@host> enrollment shape and S-36.T3's tunnel Dialer verbatim: User
// and Addr are the same fields Target already carries, never a second
// transport. RouteConfig deliberately has NO credential-shaped field —
// no private key, password or passphrase — by construction: the ssh
// private key never leaves NodeKeystore custody (see tunnel.go's
// keystoreSigner), so nothing credential-bearing is ever a candidate for
// this struct, this package's JSON persistence, or an event payload.
type RouteConfig struct {
	// User is the ssh username the route dials as.
	User string `json:"user"`
	// Addr is host:port, the same shape tunnel.go's Target.Addr dials.
	Addr string `json:"addr"`
}

// configured reports whether rc names a real route to dial.
func (rc *RouteConfig) configured() bool {
	return rc != nil && rc.User != "" && rc.Addr != ""
}

// routeNotConfiguredError (KindNotFound) reports nodeID has no usable
// Route — distinct from a route that IS configured but did not answer.
func routeNotConfiguredError(nodeID string) error {
	return cascade.Newf(cascade.KindNotFound, "nodes: node %q has no configured ssh/VPN route", nodeID)
}

// windowsRouteHint is the actionable refusal message for the
// travel-profile route probe on Windows (R-21.226), mirroring tunnel.go's
// windowsTunnelHint convention: the ssh tunnel machinery this file reuses
// is already daemon-class-refused there.
const windowsRouteHint = "cascade has no controller-side ssh/VPN route-reachability check on Windows (tier-2); " +
	"the travel-profile route probe reuses the daemon-class tunnel dial, outside the binary + headless one-shot promise (06-FORGE-SPEC §2)"

// RefuseRouteOnGOOS reports the typed tier-2 refusal for goos ==
// "windows", nil otherwise. A pure function, unit-tested against the
// literal string on every platform (Art.5), mirroring tunnel.go's
// RefuseTunnelServiceOnGOOS exactly.
func RefuseRouteOnGOOS(goos string) error {
	if goos == "windows" {
		return cascade.New(cascade.KindUnsupported, "node travel route: "+windowsRouteHint)
	}
	return nil
}

// RouteReachable reports whether rec's configured route answers, dialing
// through dialer (S-36.T3's tunnel Dialer, no new transport) and
// verifying the presented host key via verify. Three distinct outcomes,
// never collapsed:
//   - (false, routeNotConfiguredError): rec.Route is unset — nothing to
//     dial. This is "not configured," never a silent false.
//   - (false, nil): a route IS configured but the dial/handshake did not
//     succeed — "cannot reach via route," a real negative answer, not an
//     error.
//   - (true, nil): the configured route answered.
func RouteReachable(ctx context.Context, dialer Dialer, verify HostKeyVerifier, rec DeviceRecord) (bool, error) {
	if !rec.Route.configured() {
		return false, routeNotConfiguredError(rec.NodeID)
	}
	target := Target{NodeID: rec.NodeID, User: rec.Route.User, Addr: rec.Route.Addr}
	sess, err := dialer.Dial(ctx, target, verify)
	if err != nil {
		return false, nil
	}
	_ = sess.Close()
	return true, nil
}

// sshRouteChecker is the production RouteChecker (prober.go's injectable
// seam), over a real RecordStore lookup, the real tunnel Dialer, and
// KnownHosts host-key pinning — the same collaborators enroll.go's
// admission path already verifies against.
type sshRouteChecker struct {
	records    *RecordStore
	dialer     Dialer
	knownHosts *KnownHosts
	goos       string
}

// NewSSHRouteChecker returns the real RouteChecker implementation,
// replacing prober.go's always-false defaultRouteChecker (task 4).
// Exported by the wiring fix that gives it its first real caller:
// cmd/cascade/node_serve.go's composition root, which builds the same
// keystore-authenticated Dialer node_admit.go's dialForHostKey already
// uses (nodes.NewSSHDialer) and passes it here. Was package-private
// (newSSHRouteChecker) while no composition root existed to call it;
// this is the one-line export this file's own doc comment named as the
// forward seam.
func NewSSHRouteChecker(records *RecordStore, dialer Dialer, knownHosts *KnownHosts, goos string) RouteChecker {
	return &sshRouteChecker{records: records, dialer: dialer, knownHosts: knownHosts, goos: goos}
}

// Reachable implements RouteChecker. A refusal (Windows tier-2, no
// record, no configured route, or a genuine dial failure) all report
// false to this bool-shaped interface — the finer distinction lives in
// RouteReachable, which this method calls and which is independently
// tested.
func (c *sshRouteChecker) Reachable(ctx context.Context, nodeID string) bool {
	if RefuseRouteOnGOOS(c.goos) != nil {
		return false
	}
	rec, err := c.records.Get(nodeID)
	if err != nil || !rec.Route.configured() {
		return false
	}
	route := *rec.Route
	verify := func(fingerprint string) error {
		return c.knownHosts.Verify(Target{User: route.User, Addr: route.Addr}.knownHostsKey(), fingerprint, "")
	}
	ok, _ := RouteReachable(ctx, c.dialer, verify, rec)
	return ok
}

// advertisementAllowed reports whether mDNS advertisement may run under
// sec (R-21.198): travel=true disables it unconditionally, regardless of
// ScanLAN — travel wins the precedence, CI-asserted. Unexported: T1's
// still-deferred discovery.go advertiser lives in this same package, so
// this decision seam needs no export until that file lands and calls it.
func advertisementAllowed(sec Section) bool {
	if sec.Travel {
		return false
	}
	return sec.ScanLAN
}

// browseAllowed reports whether discovery browsing may run on network
// (the current interface/SSID name) under sec: ScanLAN must be true AND
// network must be present in DiscoveryNetworks — an empty allowlist
// (default) means no network qualifies, fail-closed (R-21.198), whether
// or not sec.Travel is set. Travel's own effect is specifically
// advertisementAllowed above; this function documents that browsing was
// already, independently, confined to the allowlist.
func browseAllowed(sec Section, network string) bool {
	if !sec.ScanLAN {
		return false
	}
	for _, n := range sec.DiscoveryNetworks {
		if n == network {
			return true
		}
	}
	return false
}
