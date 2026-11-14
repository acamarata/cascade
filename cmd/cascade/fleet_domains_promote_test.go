//go:build integration && !windows

// Purpose: fleet.domains.promote end to end: the probe runs in
// the daemon through the real resolver against loopback fakes and the real
// elevation middleware; the audit intent precedes the one tx and the confirm
// follows it; a crash after the intent settles unknown-outcome with no half
// tier; a pool domain gets one row per provider or none; a gateway key reads
// no vault or helper.
// Inputs: loopback fake providers, a temp topology store, an in-memory log.
// Constraints: loopback only; HOME, USERPROFILE and CASCADE_HOME in a temp dir.
// SPORT: cmd/cascade daemon registrations.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/topology"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	"github.com/acamarata/cascade/internal/providers/gateway"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// promoteElevated composes the daemon registry, with its real elevation
// middleware, over h's promoter.
func promoteElevated(t *testing.T, h *promoteHarness) elevatedDaemon {
	t.Helper()
	prev, saved := domainPromoteParts, daemonRegistrations
	domainPromoteParts = func(*daemonWiring) (*domainPromoter, error) { return h.d, nil }
	daemonRegistrations = nil
	for _, r := range saved {
		if r.Name != "completion-gate" {
			daemonRegistrations = append(daemonRegistrations, r)
		}
	}
	t.Cleanup(func() { domainPromoteParts, daemonRegistrations = prev, saved })
	return newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
}

// wantElevationRefused asserts reg refuses an unsigned promote of params.
func wantElevationRefused(t *testing.T, reg *rpc.Registry, what string, params json.RawMessage) {
	t.Helper()
	if _, eo := reg.Dispatch(owner(), &rpc.Request{JSONRPC: "2.0", Method: promoteVerb, Params: params}); eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("%s promote: %v, want elevation required", what, eo)
	}
}

// wantPromoteKind asserts err carries kind.
func wantPromoteKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	if k, _ := cascade.KindOf(err); err == nil || k != kind {
		t.Fatalf("err = %v, want kind %s", err, kind)
	}
}

func TestPromoteRunsProbeServerSide(t *testing.T) {
	m := &memLookup{provs: map[string]providerregistry.ProviderRecord{}}
	good := newFake(t, okReply)
	m.add(rec("p401", newFake(t, status(401)).srv.URL, "vault-p401"))
	m.add(rec("good", good.srv.URL, "vault-good"))
	health := &demotions{}
	res, err := providerdispatch.NewResolver(m, &countingCreds{}, runtime.NewSystemClock(),
		providertransport.NewHTTPTransport(&http.Client{}), providerdispatch.WithHealth(health))
	if err != nil {
		t.Fatal(err)
	}
	h := newPromoteHarness(t, m, res, nil)
	h.seed(t, "d401", "p401")
	h.seed(t, "dgood", "good")
	_, err = h.d.promote(owner(), raw(t, map[string]string{"domain": "d401", "tier": "paid"}), "h")
	wantPromoteKind(t, err, cascade.KindUnavailable)
	h.want(t, "d401", topology.BillingTierDiscover, 0)

	bare, _ := h.wireBare(t)
	forged := raw(t, map[string]any{"domain": "dgood", "tier": "paid", "promoted_by": "mallory", "PromotedBy": "mallory",
		"evidence": []map[string]any{{"test_outcome": "passed", "provider": "forged"}}, "principal": map[string]string{"id": "x"}})
	wantElevationRefused(t, bare, "witness-less", forged)
	d := promoteElevated(t, h)
	wantElevationRefused(t, d.Registry, "unsigned", forged)
	h.want(t, "dgood", topology.BillingTierDiscover, 0)
	if err := d.Call(context.Background(), promoteVerb, forged, nil); err != nil {
		t.Fatalf("elevated promote: %v", err)
	}
	ev := h.want(t, "dgood", topology.BillingTierPaid, 1)
	if e := ev[0]; e.PromotedBy != "owner-cli" || e.Key.Provider != "good" || e.ProbeStatus != 200 ||
		e.ProbeEndpoint != good.srv.URL || e.TestOutcome != topology.EvidenceOutcomePassed {
		t.Fatalf("evidence %+v: want the daemon's own probe row, promoted by the caller", e)
	}
	if health.n.Load() != 0 || good.key.Load() != 1 {
		t.Fatalf("probe touched health (%d demotions) or sent %d keyed calls, want 0 and 1", health.n.Load(), good.key.Load())
	}
	_, err = h.d.promote(owner(), raw(t, map[string]string{"domain": "dgood", "tier": "discover"}), "h")
	wantPromoteKind(t, err, cascade.KindInvalidInput)
	_, err = h.d.promote(owner(), raw(t, map[string]string{"domain": "dgood", "provider": "good", "tier": "paid"}), "h")
	wantPromoteKind(t, err, cascade.KindInvalidInput)

	ap := agentProbe(t, true, true, fakeBinary(t))
	h.d.probe.lanes, h.d.probe.shim, h.d.keys.Agents, h.d.keys.Shim = ap.lanes, ap.shim, ap.lanes, ap.shim
	h.rekey(t, nil)
	h.seed(t, "agent:claude", "agent:claude")
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "agent:claude", "tier": "paid"}), "h"); err != nil {
		t.Fatalf("agent promote: %v", err)
	}
	if ev := h.want(t, "agent:claude", topology.BillingTierPaid, 1); ev[0].ProbeKind != promoteKindCapabilities {
		t.Fatalf("agent evidence %+v", ev[0])
	}
}

// stubHarness is a harness over providers answered by a stub resolver.
func stubHarness(t *testing.T, fail map[string]bool, names ...string) *promoteHarness {
	t.Helper()
	reg := &promoteRegistry{provs: map[string]providerregistry.ProviderRecord{}}
	for _, n := range names {
		reg.add(providerregistry.ProviderRecord{Name: n, Driver: providerregistry.DriverAnthropic, BaseURL: "https://" + n + ".example.invalid",
			Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef("vault-" + n), KnownModels: []string{"m" + n}})
	}
	return newPromoteHarness(t, reg, &stubResolver{fail: fail}, nil)
}

// pendingPromotes returns the open promote intents.
func pendingPromotes(t *testing.T, h *promoteHarness) []audit.EffectHandle {
	t.Helper()
	pending, err := h.d.effects.PendingEffects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

// seamIntent sets afterPromoteIntent to record the open intent's key and
// the domain's state at that instant, then return ret.
func seamIntent(t *testing.T, h *promoteHarness, domain string, ret error) *string {
	t.Helper()
	key := new(string)
	afterPromoteIntent = func() error {
		p := pendingPromotes(t, h)
		if len(p) != 1 || !strings.HasPrefix(p[0].Key, promoteVerb+":") {
			t.Errorf("at the seam: %d open intents %+v, want one promote intent", len(p), p)
		} else {
			*key = p[0].Key
		}
		h.want(t, domain, topology.BillingTierDiscover, 0)
		return ret
	}
	t.Cleanup(func() { afterPromoteIntent = func() error { return nil } })
	return key
}

// effectPhase returns key's phase.
func effectPhase(t *testing.T, h *promoteHarness, key string) audit.EffectPhase {
	t.Helper()
	phase, ok, err := h.d.effects.EffectState(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("effect %q: %v, %v", key, ok, err)
	}
	return phase
}

func TestPromoteAuditIntentConfirm(t *testing.T) {
	h := stubHarness(t, map[string]bool{"mbad": true}, "a", "bad")
	h.seed(t, "da", "a")
	h.seed(t, "dbad", "bad")
	key := seamIntent(t, h, "da", nil)
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "da", "tier": "paid"}), "h"); err != nil {
		t.Fatal(err)
	}
	h.want(t, "da", topology.BillingTierPaid, 1)
	if *key == "" || effectPhase(t, h, *key) != audit.EffectConfirmed || len(pendingPromotes(t, h)) != 0 {
		t.Fatalf("intent %q not confirmed after the tx", *key)
	}
	*key = ""
	_, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "dbad", "tier": "paid"}), "h")
	wantPromoteKind(t, err, cascade.KindUnavailable)
	if *key != "" || len(pendingPromotes(t, h)) != 0 {
		t.Fatal("a failed probe recorded an intent")
	}
}

func TestPromoteCrashAfterIntent(t *testing.T) {
	h := stubHarness(t, nil, "a")
	h.seed(t, "da", "a")
	crash := cascade.New(cascade.KindInternal, "injected crash after the intent")
	key := seamIntent(t, h, "da", crash)
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "da", "tier": "paid"}), "h"); err == nil {
		t.Fatal("promote survived the crash")
	}
	if effectPhase(t, h, *key) != audit.EffectIntent {
		t.Fatal("the crashed promote left no open intent")
	}
	h.want(t, "da", topology.BillingTierDiscover, 0)
	h.wireBare(t) // the next start settles open intents
	if p := effectPhase(t, h, *key); p != audit.EffectUnknown {
		t.Fatalf("after recovery the intent is %s, want %s", p, audit.EffectUnknown)
	}
	h.want(t, "da", topology.BillingTierDiscover, 0)
	afterPromoteIntent = func() error { return nil }
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "da", "tier": "paid"}), "h"); err != nil {
		t.Fatalf("promote after recovery: %v", err)
	}
	h.want(t, "da", topology.BillingTierPaid, 1)
}

func TestPromoteWritesEvidencePerProvider(t *testing.T) {
	h := stubHarness(t, map[string]bool{"my": true}, "a", "b", "c", "x", "y")
	h.seed(t, "pool", "a", "b", "c")
	h.seed(t, "pool2", "x", "y")
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "pool", "tier": "paid"}), "h"); err != nil {
		t.Fatal(err)
	}
	ev := h.want(t, "pool", topology.BillingTierPaid, 3)
	for i, want := range []string{"a", "b", "c"} {
		if ev[i].Key.Provider != want || ev[i].DomainID != "pool" {
			t.Fatalf("row %d = %+v, want provider %s", i, ev[i], want)
		}
	}
	_, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "pool2", "tier": "paid"}), "h")
	wantPromoteKind(t, err, cascade.KindUnavailable)
	h.want(t, "pool2", topology.BillingTierDiscover, 0)
	h.seed(t, "other", "a")
	_, err = h.d.promote(owner(), raw(t, map[string]string{"provider": "a", "tier": "paid"}), "h")
	wantPromoteKind(t, err, cascade.KindConflict)
	if !rpc.IsElevated(promoteVerb, nil) || rpc.IsElevated(demoteVerb, nil) || rpc.IsElevated(forgetVerb, nil) {
		t.Fatal("only promote is elevated")
	}
	d := promoteElevated(t, h)
	if _, eo := d.Registry.Dispatch(owner(), &rpc.Request{JSONRPC: "2.0", Method: demoteVerb, Params: raw(t, map[string]string{"domain": "pool"})}); eo != nil {
		t.Fatalf("unelevated demote: %v", eo)
	}
	h.want(t, "pool", topology.BillingTierDiscover, 1) // the store's prune keeps the account's row only
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"domain": "pool", "tier": "paid"}), "h"); err != nil {
		t.Fatal(err)
	}
	h.want(t, "pool", topology.BillingTierPaid, 4)
	if _, perr := h.d.prune(context.Background()); perr != "" {
		t.Fatal(perr)
	}
	h.want(t, "pool", topology.BillingTierDiscover, 2) // b and c lost their rows: paid never stays without them
}

func TestGatewayEvidenceKeyNoVaultRead(t *testing.T) {
	reg := &promoteRegistry{provs: map[string]providerregistry.ProviderRecord{}}
	helper := filepath.Join(t.TempDir(), "no-such-helper")
	gw := gateway.Account{Name: "gw1", BaseURL: "https://gw.example.invalid", Header: gateway.HeaderBearer,
		Credential: gateway.Credential{Kind: gateway.CredentialHelper, HelperPath: helper, HelperSHA256: strings.Repeat("a", 64)}}
	reg.add(providerregistry.ProviderRecord{Name: "gwp", Driver: providerregistry.DriverAnthropic, BaseURL: gw.BaseURL,
		Auth: providerregistry.AuthGateway, AuthRef: "gateway:gw1", KnownModels: []string{"m"}})
	res := &stubResolver{}
	h := newPromoteHarness(t, reg, res, []gateway.Account{gw})
	h.seed(t, "dg", "gwp")
	if _, err := h.d.promote(owner(), raw(t, map[string]string{"provider": "gwp", "tier": "paid"}), "hash"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	ev := h.want(t, "dg", topology.BillingTierPaid, 1)
	if ev[0].Key.Provider != "gwp" || len(ev[0].Key.CredentialFingerprint) != 32 || res.calls.Load() != 1 {
		t.Fatalf("evidence %+v after %d resolves, want one gateway-keyed row", ev[0], res.calls.Load())
	}
	if n, perr := h.d.prune(context.Background()); n != 0 || perr != "" {
		t.Fatalf("prune = %d, %q; want 0 and no error", n, perr)
	}
	if n := h.creds.n.Load(); n != 0 {
		t.Fatalf("gateway evidence key read the vault %d times, want 0", n)
	}
	if _, err := os.Stat(helper); !os.IsNotExist(err) {
		t.Fatalf("helper path touched: %v", err)
	}
}
