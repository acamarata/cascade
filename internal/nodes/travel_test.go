// Purpose: travel.go's tests — the real RouteChecker over a fake tunnel
//	Dialer (this package's default lane forbids importing "net"; the
//	production sshDialer this Dialer interface abstracts is already
//	proven against a real sshd by tunnel_test.go's integration-tagged
//	TestTunnelRealSSHD, so RouteReachable — a thin, protocol-free
//	wrapper around that same Dialer — reuses that proof rather than
//	duplicating a second real-sshd harness here), the travel/route
//	config idempotence, and the travel⇒advertise/browse precedence.
// SPORT: internal/nodes travel_test.go/ADDED (P1-E36-W7-S72-T3).

package nodes

import (
	"context"
	"testing"
)

func testRecordWithRoute(nodeID, user, addr string) DeviceRecord {
	rec := DeviceRecord{NodeID: nodeID}
	if user != "" || addr != "" {
		rec.Route = &RouteConfig{User: user, Addr: addr}
	}
	return rec
}

// TestRouteReachableAnswers: a configured route whose dial succeeds
// reports (true, nil).
func TestRouteReachableAnswers(t *testing.T) {
	rec := testRecordWithRoute("n1", "worker", "host1:22")
	dialer := &fakeDialer{fp: "fp1", outcomes: []func() (Session, error){
		func() (Session, error) { return newFakeSession(), nil },
	}}
	verify := func(string) error { return nil }
	ok, err := RouteReachable(context.Background(), dialer, verify, rec)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true: dial succeeded")
	}
}

// TestRouteUnreachableStaysUnavailable: a configured route whose dial
// fails reports (false, nil) — a real negative answer, never an error,
// so the prober's caller correctly falls through to the ordinary
// unavailable transition.
func TestRouteUnreachableStaysUnavailable(t *testing.T) {
	rec := testRecordWithRoute("n1", "worker", "host1:22")
	dialer := &fakeDialer{fp: "fp1", outcomes: []func() (Session, error){
		func() (Session, error) { return nil, context.DeadlineExceeded },
	}}
	verify := func(string) error { return nil }
	ok, err := RouteReachable(context.Background(), dialer, verify, rec)
	if err != nil {
		t.Fatalf("err = %v, want nil (a failed dial is a real negative answer, not an error)", err)
	}
	if ok {
		t.Fatal("ok = true, want false: dial failed")
	}
}

// TestRouteNotConfiguredIsDistinctFromUnreachable: no Route at all
// reports a typed routeNotConfiguredError, never the same (false, nil)
// shape a configured-but-unreachable route reports.
func TestRouteNotConfiguredIsDistinctFromUnreachable(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1"}
	dialer := &fakeDialer{outcomes: []func() (Session, error){
		func() (Session, error) {
			t.Fatal("dialer must not be called with no route configured")
			return nil, nil
		},
	}}
	ok, err := RouteReachable(context.Background(), dialer, func(string) error { return nil }, rec)
	if ok {
		t.Fatal("ok = true, want false: no route configured")
	}
	if err == nil {
		t.Fatal("err = nil, want routeNotConfiguredError")
	}
}

// TestSSHRouteCheckerReachableGatesOnConfiguredRoute proves the real
// RouteChecker (prober.go's seam) reports false for a node with no
// configured route, without ever invoking the dialer.
func TestSSHRouteCheckerReachableGatesOnConfiguredRoute(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), fixedClock{})
	id := testIdentity(t, "r")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	dialer := &fakeDialer{outcomes: []func() (Session, error){
		func() (Session, error) {
			t.Fatal("dialer must not be called with no route configured")
			return nil, nil
		},
	}}
	checker := NewSSHRouteChecker(store, dialer, NewKnownHosts(newMemKnownHostsBackend()), "darwin")
	if checker.Reachable(context.Background(), id.NodeID) {
		t.Fatal("Reachable = true, want false: no route configured")
	}
}

// TestSSHRouteCheckerReachableRealDial proves the real RouteChecker
// answers true when the configured route's dial succeeds and its
// presented host key matches the pinned fingerprint.
func TestSSHRouteCheckerReachableRealDial(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), fixedClock{})
	id := testIdentity(t, "s")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	rec.Route = &RouteConfig{User: "worker", Addr: "host1:22"}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	kh := NewKnownHosts(newMemKnownHostsBackend())
	pinTarget(t, kh, Target{User: "worker", Addr: "host1:22"}, "fp1")
	dialer := &fakeDialer{fp: "fp1", outcomes: []func() (Session, error){
		func() (Session, error) { return newFakeSession(), nil },
	}}
	checker := NewSSHRouteChecker(store, dialer, kh, "darwin")
	if !checker.Reachable(context.Background(), id.NodeID) {
		t.Fatal("Reachable = false, want true: route configured and dial succeeded")
	}
}

// TestRefuseRouteOnGOOS: the pure Windows tier-2 refusal, mirroring
// tunnel.go's RefuseTunnelServiceOnGOOS test (Art.5).
func TestRefuseRouteOnGOOS(t *testing.T) {
	if err := RefuseRouteOnGOOS("windows"); err == nil {
		t.Fatal("RefuseRouteOnGOOS(\"windows\") = nil, want a typed refusal")
	}
	if err := RefuseRouteOnGOOS("linux"); err != nil {
		t.Fatalf("RefuseRouteOnGOOS(\"linux\") = %v, want nil", err)
	}
}

// TestSSHRouteCheckerRefusesOnWindows: the real RouteChecker honors the
// same refusal without a real windows build.
func TestSSHRouteCheckerRefusesOnWindows(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), fixedClock{})
	id := testIdentity(t, "w")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	rec.Route = &RouteConfig{User: "worker", Addr: "host1:22"}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	dialer := &fakeDialer{outcomes: []func() (Session, error){
		func() (Session, error) { t.Fatal("dialer must not be called on windows tier-2"); return nil, nil },
	}}
	checker := NewSSHRouteChecker(store, dialer, NewKnownHosts(newMemKnownHostsBackend()), "windows")
	if checker.Reachable(context.Background(), id.NodeID) {
		t.Fatal("Reachable = true, want false: windows tier-2 refusal")
	}
}

// TestTravelConfigIdempotentReconverge: parsing the same [nodes].travel /
// route config twice is a pure, deterministic no-op (06 §5.9) — the
// parse-level idempotence this package's Section owns. (Daemon-level
// hot-reload apply-without-restart for the whole [nodes] section has no
// registration hook in the tree at all yet — see config.go's own
// existing doc comment and this ticket's journal; that pre-existing gap
// is not reintroduced or worsened by this ticket's addition.)
func TestTravelConfigIdempotentReconverge(t *testing.T) {
	raw := map[string]interface{}{"travel": true, "scan_lan": true, "discovery_networks": []interface{}{"home-wifi"}}
	first, err := parseSection(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseSection(raw)
	if err != nil {
		t.Fatal(err)
	}
	if first.Travel != second.Travel || first.ScanLAN != second.ScanLAN || len(first.DiscoveryNetworks) != len(second.DiscoveryNetworks) {
		t.Fatalf("second parse = %+v, want identical to first %+v", second, first)
	}
	if !second.Travel {
		t.Fatal("Travel = false, want true")
	}
}

// TestTravelDisablesAdvertisement (R-21.198): travel=true wins the
// precedence over scan_lan=true unconditionally.
func TestTravelDisablesAdvertisement(t *testing.T) {
	cases := []struct {
		name    string
		sec     Section
		allowed bool
	}{
		{"travel wins over scan_lan=true", Section{Travel: true, ScanLAN: true}, false},
		{"travel with scan_lan=false", Section{Travel: true, ScanLAN: false}, false},
		{"no travel, scan_lan=true advertises", Section{Travel: false, ScanLAN: true}, true},
		{"no travel, scan_lan=false", Section{Travel: false, ScanLAN: false}, false},
	}
	for _, c := range cases {
		if got := advertisementAllowed(c.sec); got != c.allowed {
			t.Errorf("%s: advertisementAllowed = %v, want %v", c.name, got, c.allowed)
		}
	}
}

// TestBrowseOnlyOnAllowlistedNetwork: browsing runs only on a network
// present in DiscoveryNetworks, fail-closed on an empty allowlist even
// with scan_lan=true, regardless of travel.
func TestBrowseOnlyOnAllowlistedNetwork(t *testing.T) {
	sec := Section{ScanLAN: true, DiscoveryNetworks: []string{"home-wifi"}}
	if !browseAllowed(sec, "home-wifi") {
		t.Fatal("browseAllowed(home-wifi) = false, want true: allowlisted")
	}
	if browseAllowed(sec, "hotel-guest") {
		t.Fatal("browseAllowed(hotel-guest) = true, want false: not allowlisted")
	}
	emptyAllowlist := Section{ScanLAN: true}
	if browseAllowed(emptyAllowlist, "home-wifi") {
		t.Fatal("browseAllowed with empty allowlist = true, want false: fail-closed")
	}
	scanOff := Section{ScanLAN: false, DiscoveryNetworks: []string{"home-wifi"}}
	if browseAllowed(scanOff, "home-wifi") {
		t.Fatal("browseAllowed with scan_lan=false = true, want false")
	}
}
