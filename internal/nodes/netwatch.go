// Purpose: R-16.67's network-change detector: a 30 s poll of
//	net.Interfaces() plus a default-route probe, on every platform, pure
//	Go with no CGO and no per-platform files.
// Inputs: the injected Clock/Ticker, an interfaces-lister and a
//	default-route prober (both swappable seams so tests never touch a
//	real NIC).
// Outputs: a nodes.network.changed event on every detected change, plus
//	an immediate out-of-cycle probe trigger (prober.go) — never a config
//	write, never a re-enrollment, never a teardown of S-36.T3's own
//	reconnect state machine (R-16.19: never reconfiguration).
// Constraints: no bare time.Now/Sleep (Art.7.3); no platform build tags —
//	net.Interfaces() and a UDP dial-for-routing-only probe are both
//	already portable across darwin/linux/windows.
// SPORT: internal/nodes NetworkWatcher/ADDED (P1-E36-W7-S72-T2).

package nodes

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/events"
)

// IfaceInfo is the minimal, fake-friendly shape NetworkWatcher needs from
// one network interface: its name and its bound address strings. Defined
// locally rather than using net.Interface directly because
// net.Interface.Addrs() makes a real OS call keyed by interface index —
// a struct literal built by a test cannot fake that call, which would
// silently make every "fake" interfaces list still hit the real NIC.
type IfaceInfo struct {
	Name  string
	Addrs []string
}

// InterfacesFunc lists the host's network interfaces. systemInterfaces
// is the production implementation; tests inject a fake returning
// IfaceInfo values directly, with no real syscall involved.
type InterfacesFunc func() ([]IfaceInfo, error)

// systemInterfaces is the production InterfacesFunc: net.Interfaces()
// plus each interface's own real Addrs() call, flattened into IfaceInfo.
// An interface whose Addrs() errors is included with no addresses rather
// than aborting the whole fingerprint — one flaky interface must not
// blind the watcher to every other change.
func systemInterfaces() ([]IfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]IfaceInfo, 0, len(ifaces))
	for _, iface := range ifaces {
		info := IfaceInfo{Name: iface.Name}
		if addrs, err := iface.Addrs(); err == nil {
			for _, a := range addrs {
				info.Addrs = append(info.Addrs, a.String())
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// DefaultRouteFunc reports whether a default route currently resolves.
// defaultRouteProbe is the production implementation (a UDP dial that
// only consults the routing table and sends no packet); tests inject a
// fake.
type DefaultRouteFunc func() bool

// defaultRouteProbe resolves the local outbound address net.Dial would
// pick for a UDP socket. UDP dial never sends a packet or touches the
// network — the kernel only performs a routing-table lookup — so this is
// a safe, side-effect-free default-route check portable across every
// platform this repo targets, with no CGO and no platform-specific API.
func defaultRouteProbe() bool {
	conn, err := net.Dial("udp", "198.51.100.1:9")
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr() != nil
}

// nodesNetworkChangedNamespace mirrors nodesNamespace (prober.go); kept
// distinct in name only for readability at call sites in this file.
const nodesNetworkChangedNamespace = nodesNamespace

// NetworkChangedKind is the nodes.network.changed event's kind (R-16.67).
const NetworkChangedKind events.EventKind = "nodes.network.changed"

// NetworkWatcherDeps carries every collaborator NetworkWatcher needs.
type NetworkWatcherDeps struct {
	Ticker       Ticker
	Interfaces   InterfacesFunc   // nil defaults to net.Interfaces
	DefaultRoute DefaultRouteFunc // nil defaults to defaultRouteProbe
	Bus          EventBus         // nil is a documented no-op
	// OnChange is called after a detected change is published — the
	// out-of-cycle probe trigger. Prober.TriggerOutOfCycle satisfies
	// this signature directly.
	OnChange func()
}

// NetworkWatcher polls Interfaces()+DefaultRoute() once per Ticker tick
// and detects any change in the resulting fingerprint from the previous
// tick. It never reconfigures anything (R-16.19): its only actions are
// publishing an event and invoking OnChange.
type NetworkWatcher struct {
	deps NetworkWatcherDeps
	last string
	have bool
}

// NewNetworkWatcher returns a ready NetworkWatcher, applying production
// defaults for any nil seam.
func NewNetworkWatcher(deps NetworkWatcherDeps) *NetworkWatcher {
	if deps.Interfaces == nil {
		deps.Interfaces = systemInterfaces
	}
	if deps.DefaultRoute == nil {
		deps.DefaultRoute = defaultRouteProbe
	}
	return &NetworkWatcher{deps: deps}
}

// Run drives the poll loop until ctx is canceled.
func (w *NetworkWatcher) Run(ctx context.Context) {
	defer w.deps.Ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.deps.Ticker.C():
			w.pollOnce(ctx)
		}
	}
}

// pollOnce runs one fingerprint comparison; exported as a method (rather
// than inlined in Run) so tests can drive single passes deterministically
// without a real Ticker channel.
func (w *NetworkWatcher) pollOnce(ctx context.Context) {
	fp, err := w.fingerprint()
	if err != nil {
		return
	}
	if w.have && fp == w.last {
		return
	}
	w.last = fp
	w.have = true
	w.emitChanged(ctx)
	if w.deps.OnChange != nil {
		w.deps.OnChange()
	}
}

// fingerprint builds a deterministic, order-independent string
// identifying the current set of interface name+addresses plus the
// default-route state, so an unrelated field reordering from
// net.Interfaces() never registers as spurious churn.
func (w *NetworkWatcher) fingerprint() (string, error) {
	ifaces, err := w.deps.Interfaces()
	if err != nil {
		return "", err
	}
	var entries []string
	for _, iface := range ifaces {
		addrStrs := append([]string(nil), iface.Addrs...)
		sort.Strings(addrStrs)
		entries = append(entries, iface.Name+"="+strings.Join(addrStrs, ","))
	}
	sort.Strings(entries)
	route := "no-default-route"
	if w.deps.DefaultRoute() {
		route = "has-default-route"
	}
	entries = append(entries, route)
	return strings.Join(entries, ";"), nil
}

func (w *NetworkWatcher) emitChanged(ctx context.Context) {
	if w.deps.Bus == nil {
		return
	}
	raw, err := json.Marshal(struct {
		Fingerprint string `json:"fingerprint"`
	}{Fingerprint: w.last})
	if err != nil {
		return
	}
	_, _ = w.deps.Bus.Publish(ctx, nodesNetworkChangedNamespace, NetworkChangedKind, "nodes.netwatch", raw)
}
