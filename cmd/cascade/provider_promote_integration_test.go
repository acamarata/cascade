//go:build integration && !windows

// Purpose: `provider test --promote` and the evidence forget of `provider
// remove` end to end. The primary test composes the whole daemon
// (composeDaemon) and serves its own server on the socket the CLI resolves;
// the CLI dials through its production client and callElevated, with only
// the signing custody a counting test double. The others drive a real
// elevation-checked registry over the real key function and topology store.
// Inputs: a loopback fake provider, a temp data directory, a credential
// source the test rotates. Outputs: none (tests).
// Constraints: loopback only; HOME, USERPROFILE and CASCADE_HOME in a temp dir.
// SPORT: cmd/cascade provider promote tests.
package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/fleet/topology"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// rotatingCreds is the vault: one value per reference the test can change.
type rotatingCreds struct {
	mu  sync.Mutex
	val string
}

func (c *rotatingCreds) Resolve(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, nil
}

func (c *rotatingCreds) set(v string) {
	c.mu.Lock()
	c.val = v
	c.mu.Unlock()
}

// countingKeystore counts Sign calls of the test custody's key.
type countingKeystore struct {
	signingKeystore
	signs *atomic.Int32
}

func (k countingKeystore) Sign(data []byte) ([]byte, error) {
	k.signs.Add(1)
	return k.signingKeystore.Sign(data)
}

// promoteCLI is one provider "good" in the CLI's registry, one daemon
// registry over the same registry file, and the seams that connect them.
type promoteCLI struct {
	h     *promoteHarness
	d     elevatedDaemon
	deps  providerDeps
	creds *rotatingCreds
	signs *atomic.Int32
	dials atomic.Int32
	up    atomic.Bool
}

// liveURL is a loopback server's URL; deadURL is one nothing listens on.
func liveURL(t *testing.T) string { return newFake(t, body("{}")).srv.URL }

func deadURL(t *testing.T) string { f := newFake(t, body("{}")); f.srv.Close(); return f.srv.URL }

// addGood registers (or re-registers) provider "good" at url in the CLI's
// registry, with one lane.
func (f *promoteCLI) addGood(t *testing.T, url string) {
	t.Helper()
	store, err := openProviderStorage(context.Background(), f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rec := sampleRegistryProvider("good", providerregistry.HealthUnknown)
	rec.BaseURL = url
	seedCapProvider(t, store.Registry, rec, providerregistry.LaneRecord{LaneName: "good-lane"})
}

// newPromoteCLI wires the fixture; the daemon starts up.
func newPromoteCLI(t *testing.T, url string) *promoteCLI {
	t.Helper()
	f := &promoteCLI{deps: testProviderDeps(t, nil), creds: &rotatingCreds{val: "v1"}, signs: new(atomic.Int32)}
	f.deps.Registry = nil // the durable providers.db, as in production
	f.addGood(t, url)
	// The daemon's own handle on providers.db only: the events store is
	// exclusive to one handle, and the CLI opens it per command.
	regDB, err := openMigratedDB(context.Background(), filepath.Join(f.deps.Paths.DataDir(), providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), "", "")
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regDB.Close() })
	view := providerregistry.NewRegistry(regDB, runtime.NewSystemClock())
	f.h = newPromoteHarness(t, view, &stubResolver{}, nil)
	f.h.d.keys.Credentials = f.creds
	f.h.rekey(t, nil)
	f.h.seed(t, "dgood", "good")
	f.d = promoteElevated(t, f.h)
	socket := serveRegistry(t, f.d.Registry)
	f.up.Store(true)

	prevDial, prevCall := providerDaemonDial, providerPromoteCallDeps
	providerDaemonDial = func(ctx context.Context, _ string) (net.Conn, error) {
		f.dials.Add(1)
		if !f.up.Load() {
			return nil, errors.New("dial unix: connect: connection refused")
		}
		return client.UnixDialer(ctx, socket)
	}
	custody := testCustody(t, countingKeystore{f.d.KS, f.signs}, elevation.CustodyPlatform)
	providerPromoteCallDeps = func(stderr io.Writer) elevatedCallDeps {
		return elevatedCallDeps{Custody: custody, Clock: f.h.d.probe.clock, Getenv: func(string) string { return "" }, Stderr: stderr}
	}
	t.Cleanup(func() { providerDaemonDial, providerPromoteCallDeps = prevDial, prevCall })
	return f
}

// reconcile runs the daemon's prune, as a reconciled pass would.
func (f *promoteCLI) reconcile(t *testing.T) {
	t.Helper()
	if _, perr := f.h.d.prune(context.Background()); perr != "" {
		t.Fatalf("prune: %s", perr)
	}
}

// composedPromote is one composed daemon, its fleet's topology store, the
// CLI deps over the same paths and the loopback provider "good" behind it.
type composedPromote struct {
	ce    composedElevated
	topo  *topology.Store
	deps  providerDeps
	fake  *fakeProvider
	signs *atomic.Int32
}

// newComposedPromote composes the daemon, stores good's key in the daemon's
// vault with a standing grant, registers and reconciles good, and serves
// the composed server on its own socket.
func newComposedPromote(t *testing.T) *composedPromote {
	t.Helper()
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	seen := observeWiring(t)
	f := &composedPromote{ce: composeElevated(t, true), fake: newFake(t, body(oneToken)), signs: new(atomic.Int32)}
	w, ctx, paths := <-seen, context.Background(), f.ce.Deps.Paths
	f.topo = w.Deps.Fleet.Topology()
	fw, err := bindDaemonFirewall(paths)
	if err == nil {
		_, err = fw.Broker().Set(ctx, "vault-good", []byte(probeValue), secrets.SetUpdate)
	}
	store, gerr := secrets.NewFileGrantStore(paths.DataDir())
	grants, nerr := secrets.NewGrants(store, runtime.NewSystemClock())
	if err = cmp.Or(err, gerr, nerr); err == nil {
		_, err = grants.Issue(ctx, "vault-good", 10*time.Minute)
	}
	if good := rec("good", f.fake.srv.URL, "vault-good"); err == nil {
		good.Tier = providerregistry.TierStrong
		seedCapProvider(t, w.Deps.Fleet.CapacityParts().Registry, good, providerregistry.LaneRecord{LaneName: "good-lane"})
		_, err = w.Deps.Fleet.ReconcileNow(ctx)
	}
	l, lerr := runtime.ListenOwnerSocket(f.ce.Opts.Settings.SocketPath)
	if err = cmp.Or(err, lerr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.ce.Opts.Server.Serve(l) }()
	t.Cleanup(func() { _ = f.ce.Opts.Server.Close() })
	f.deps = testProviderDeps(t, nil)
	f.deps.Paths, f.deps.Registry = paths, nil
	custody := testCustody(t, countingKeystore{f.ce.KS, f.signs}, elevation.CustodyPlatform)
	prev := providerPromoteCallDeps
	providerPromoteCallDeps = func(stderr io.Writer) elevatedCallDeps {
		return elevatedCallDeps{Custody: custody, Clock: runtime.NewSystemClock(), Getenv: func(string) string { return "" }, Stderr: stderr}
	}
	t.Cleanup(func() { providerPromoteCallDeps = prev })
	return f
}

// domain is the one quota domain the reconcile gave good.
func (f *composedPromote) domain(t *testing.T) string {
	t.Helper()
	creds, err := f.topo.ListCredentials(context.Background())
	for _, c := range creds {
		if c.AccountRef == "good" {
			return string(c.QuotaDomainRef)
		}
	}
	t.Fatalf("no reconciled credential for good (err %v) in %+v", err, creds)
	return ""
}

func TestProviderTestPromoteIsElevated(t *testing.T) {
	f := newComposedPromote(t)
	domain, view := f.domain(t), &promoteHarness{topo: f.topo}
	wantElevationRefused(t, f.ce.Registry, "witness-less", raw(t, map[string]string{"provider": "good", "tier": "paid"}))
	view.want(t, domain, topology.BillingTierDiscover, 0)

	stdout, _, err := runProvider(t, f.deps, "test", "good", "--promote", "paid")
	if err != nil {
		t.Fatalf("provider test --promote paid: %v", err)
	}
	ev := view.want(t, domain, topology.BillingTierPaid, 1)
	if ev[0].Key.Provider != "good" || f.signs.Load() != 1 || f.fake.key.Load() == 0 {
		t.Fatalf("evidence %+v, signs %d, keyed probes %d: want good's row, one Sign, a keyed daemon probe",
			ev[0], f.signs.Load(), f.fake.key.Load())
	}
	for _, want := range []string{domain, "tier=paid", "provider good"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output %q lacks %q", stdout, want)
		}
	}
	if strings.Contains(stdout, probeValue) {
		t.Fatalf("output leaks a credential: %q", stdout)
	}
}

func TestProviderTestPromoteFailedTestWritesNothing(t *testing.T) {
	f := newPromoteCLI(t, deadURL(t))
	_, _, err := runProvider(t, f.deps, "test", "good", "--promote", "paid")
	if !cascade.HasKind(err, cascade.KindUnavailable) || err == nil || !strings.Contains(err.Error(), `"good" is not reachable`) {
		t.Fatalf("failed local test: err = %v, want unavailable naming the provider", err)
	}
	if f.dials.Load() != 0 || f.signs.Load() != 0 {
		t.Fatalf("a failed test sent %d daemon dials and %d signatures, want 0", f.dials.Load(), f.signs.Load())
	}
	f.h.want(t, "dgood", topology.BillingTierDiscover, 0)
}

// promoteGood promotes through the CLI and checks the one row.
func (f *promoteCLI) promoteGood(t *testing.T) {
	t.Helper()
	if _, _, err := runProvider(t, f.deps, "test", "good", "--promote", "paid"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	f.h.want(t, "dgood", topology.BillingTierPaid, 1)
}

func TestReaddedProviderStartsUnpromoted(t *testing.T) {
	f := newPromoteCLI(t, liveURL(t))
	f.promoteGood(t)
	f.up.Store(false) // remove with the daemon down: the rows stay
	stdout, _, err := runProvider(t, f.deps, "remove", "good")
	if err != nil || !strings.Contains(stdout, "invalid") || !strings.Contains(stdout, "next reconcile") {
		t.Fatalf("remove with the daemon down: err=%v out=%q, want the invalid-evidence note", err, stdout)
	}
	f.h.want(t, "dgood", topology.BillingTierPaid, 1)
	time.Sleep(3 * time.Millisecond) // the instance id carries the creation millisecond
	f.addGood(t, liveURL(t))
	f.reconcile(t)
	f.h.want(t, "dgood", topology.BillingTierDiscover, 0)
}

func TestRemovedProviderEvidenceDeleted(t *testing.T) {
	f := newPromoteCLI(t, liveURL(t))
	f.promoteGood(t)
	if stdout, _, err := runProvider(t, f.deps, "remove", "good"); err != nil || strings.Contains(stdout, "note") {
		t.Fatalf("remove with the daemon up: err=%v out=%q", err, stdout)
	}
	if rows, err := f.h.topo.BillingEvidenceFor(context.Background(), "dgood"); err != nil || len(rows) != 0 {
		t.Fatalf("after remove: %d rows, err %v, want 0", len(rows), err)
	}
	f.reconcile(t)
	f.h.want(t, "dgood", topology.BillingTierDiscover, 0)
}

func TestRotatedCredentialInvalidatesEvidence(t *testing.T) {
	f := newPromoteCLI(t, liveURL(t))
	f.promoteGood(t)
	f.reconcile(t) // unchanged: the row still matches
	f.h.want(t, "dgood", topology.BillingTierPaid, 1)
	f.creds.set("v2")
	f.reconcile(t)
	f.h.want(t, "dgood", topology.BillingTierDiscover, 0)
}
