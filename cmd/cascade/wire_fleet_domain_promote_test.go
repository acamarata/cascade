//go:build !windows

// Purpose: fixtures for the fleet.domains verbs and the
// no-network legs: a prune denial keeps every row, names the provider in
// prune_error and never fails the demote, the next reconciled pass deletes
// only a removed provider's rows.
// Inputs: a migrated topology store under t.TempDir(), an in-memory audit
// log, an in-memory registry view and a stub resolver.
// Constraints: no network; HOME, USERPROFILE and CASCADE_HOME in a temp dir.
// SPORT: cmd/cascade daemon registrations.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/gateway"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// promoteCanary is the runtime-built credential value; it must never leak.
var promoteCanary = "canary-" + strings.Repeat("c", 12)

// promoteRegistry is an in-memory registry view.
type promoteRegistry struct {
	provs map[string]providerregistry.ProviderRecord
	lanes []providerregistry.LaneRecord
}

func (r *promoteRegistry) ListLanes(context.Context) ([]providerregistry.LaneRecord, error) {
	return append([]providerregistry.LaneRecord(nil), r.lanes...), nil
}

func (r *promoteRegistry) GetProvider(_ context.Context, name string) (providerregistry.ProviderRecord, error) {
	rec, ok := r.provs[name]
	if !ok {
		return rec, cascade.Newf(cascade.KindNotFound, "provider %q not found", name)
	}
	return rec, nil
}

func (r *promoteRegistry) UpsertLane(context.Context, providerregistry.LaneRecord) error { return nil }

func (r *promoteRegistry) add(rec providerregistry.ProviderRecord) {
	r.provs[rec.Name] = rec
	r.lanes = append(r.lanes, providerregistry.LaneRecord{LaneName: rec.Name + "-lane", ProviderName: rec.Name,
		Weight: 1, Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable})
}

// promoteCreds answers every vault reference with the canary and counts.
type promoteCreds struct{ n atomic.Int32 }

func (c *promoteCreds) Resolve(context.Context, string) (string, error) {
	c.n.Add(1)
	return promoteCanary, nil
}

// stubResolver answers one-token chats without a network; fail lists the
// providers (by selection model) whose chat fails.
type stubResolver struct {
	fail  map[string]bool
	calls atomic.Int32
}

type stubModel struct {
	provider.ModelProvider
	fail bool
}

func (s *stubResolver) Resolve(_ context.Context, sel provider.Selection) (provider.ModelProvider, error) {
	s.calls.Add(1)
	return stubModel{fail: s.fail[sel.Model]}, nil
}

func (m stubModel) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	if m.fail {
		return provider.ChatResponse{}, cascade.New(cascade.KindUnavailable, "stub: 401")
	}
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "p"}, Usage: provider.Usage{OutputTokens: 1}}, nil
}

// promoteHarness is one promoter over a fresh topology store.
type promoteHarness struct {
	d     *domainPromoter
	topo  *topology.Store
	creds *promoteCreds
	logs  *bytes.Buffer
	home  string
}

// newPromoteHarness builds the promoter over reg and res with the real key fn.
func newPromoteHarness(t *testing.T, reg promoteLookup, res promoteResolver, gws []gateway.Account) *promoteHarness {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	clock := runtime.NewSystemClock()
	db, err := sql.Open("sqlite", filepath.Join(home, "topology.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := topology.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), clock, nil))
	if err != nil {
		t.Fatal(err)
	}
	h := &promoteHarness{topo: topology.NewStore(db), creds: &promoteCreds{}, logs: &bytes.Buffer{}, home: home}
	h.d = &domainPromoter{topo: h.topo, probe: promoteProbe{lookup: reg, resolve: res, clock: clock}, effects: effects,
		keys:   evidenceKeyDeps{Registry: reg, Credentials: h.creds, Gateways: gws, KeyPath: filepath.Join(home, "evidence.key")},
		logger: slog.New(slog.NewTextHandler(h.logs, nil))}
	h.rekey(t, gws)
	return h
}

// rekey rebuilds the key fn over gws (the admitted gateway accounts).
func (h *promoteHarness) rekey(t *testing.T, gws []gateway.Account) {
	t.Helper()
	h.d.keys.Gateways = gws
	fn, err := newEvidenceKeyFn(h.d.keys)
	if err != nil {
		t.Fatal(err)
	}
	h.d.key = fn
}

// seed writes domain with one account and credential per provider.
func (h *promoteHarness) seed(t *testing.T, domain string, providers ...string) {
	t.Helper()
	ctx := context.Background()
	for i, p := range providers {
		err := h.topo.UpsertAccount(ctx, topology.Account{ID: topology.AccountID(p), Provider: "ollama",
			Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce})
		if err == nil && i == 0 {
			err = h.topo.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: topology.DomainID(domain), AccountRef: topology.AccountID(p),
				Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierDiscover})
		}
		if err == nil {
			err = h.topo.UpsertRuntimeProfile(ctx, topology.RuntimeProfile{ID: topology.RuntimeProfileID(p + ":rt"), Runtime: topology.RuntimeAPI})
		}
		if err == nil {
			err = h.topo.UpsertCredential(ctx, topology.Credential{ID: topology.CredentialID(domain + "-" + p), AccountRef: topology.AccountID(p),
				QuotaDomainRef: topology.DomainID(domain), RuntimeProfileRef: topology.RuntimeProfileID(p + ":rt"), Health: topology.CredentialOK})
		}
		if err != nil {
			t.Fatalf("seed %s/%s: %v", domain, p, err)
		}
	}
}

// want asserts domain's tier and evidence rows; it returns the rows.
func (h *promoteHarness) want(t *testing.T, domain string, tier topology.BillingTier, rows int) []topology.BillingEvidence {
	t.Helper()
	q, err := h.topo.GetQuotaDomain(context.Background(), topology.DomainID(domain))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := h.topo.BillingEvidenceFor(context.Background(), topology.DomainID(domain))
	if err != nil || q.BillingTier != tier || len(ev) != rows {
		t.Fatalf("%s: tier=%s rows=%d err=%v, want tier=%s rows=%d", domain, q.BillingTier, len(ev), err, tier, rows)
	}
	return ev
}

// wireBare mounts the verbs on a registry with no elevation middleware.
func (h *promoteHarness) wireBare(t *testing.T) (*rpc.Registry, *events.Bus) {
	t.Helper()
	prev := domainPromoteParts
	domainPromoteParts = func(*daemonWiring) (*domainPromoter, error) { return h.d, nil }
	t.Cleanup(func() { domainPromoteParts = prev })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := runtime.NewSystemClock()
	w := &daemonWiring{Ctx: ctx, Registry: rpc.NewRegistry(), Bus: events.New(storetest.NewMemStore(), clock), Clock: clock,
		Manifest: daemon.NewManifest(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), clock)}
	if err := wireFleetDomainPromote(w); err != nil {
		t.Fatal(err)
	}
	return w.Registry, w.Bus
}

// owner is a context carrying the owner CLI caller.
func owner() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

// raw marshals v.
func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPruneErrorKeepsEvidence(t *testing.T) {
	reg := &promoteRegistry{provs: map[string]providerregistry.ProviderRecord{}}
	gw := gateway.Account{Name: "gwp", BaseURL: "https://gw.example.invalid", Header: gateway.HeaderBearer,
		Credential: gateway.Credential{Kind: gateway.CredentialEnv, EnvVar: "PROMOTE_TEST_TOKEN"}}
	p := providerregistry.ProviderRecord{Name: "p", Driver: providerregistry.DriverAnthropic, BaseURL: gw.BaseURL,
		Auth: providerregistry.AuthGateway, AuthRef: "gateway:gwp", KnownModels: []string{"mp"}}
	q := providerregistry.ProviderRecord{Name: "q", Driver: providerregistry.DriverAnthropic, BaseURL: "https://q.example.invalid",
		Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef("vault-q"), KnownModels: []string{"mq"}}
	reg.add(p)
	reg.add(q)
	h := newPromoteHarness(t, reg, &stubResolver{}, []gateway.Account{gw})
	h.seed(t, "dp", "p")
	h.seed(t, "dq", "q")
	ctx := owner()
	for _, dom := range []string{"dp", "dq"} {
		if _, err := h.d.promote(ctx, raw(t, map[string]string{"domain": dom, "tier": "paid"}), "hash"); err != nil {
			t.Fatalf("promote %s: %v", dom, err)
		}
	}
	reg2, bus := h.wireBare(t)
	h.rekey(t, nil) // p's gateway account is no longer admitted: its key is a permission denial
	res, eo := reg2.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: demoteVerb, Params: raw(t, map[string]string{"domain": "dq"})})
	if eo != nil {
		t.Fatalf("demote failed on a prune error: %v", eo)
	}
	out, _ := json.Marshal(res)
	var reply struct {
		Tier       string `json:"tier"`
		PruneError string `json:"prune_error"`
	}
	_ = json.Unmarshal(out, &reply)
	if reply.Tier != "discover" || !strings.Contains(reply.PruneError, `"p"`) {
		t.Fatalf("demote reply %s: want tier discover and a prune_error naming p", out)
	}
	h.want(t, "dp", topology.BillingTierPaid, 1)
	h.want(t, "dq", topology.BillingTierDiscover, 1)
	logs := h.logs.String()
	if n := strings.Count(logs, "fleet domain evidence prune failed"); n != 1 {
		t.Fatalf("prune error logged %d times, want 1:\n%s", n, logs)
	}
	if strings.Contains(logs+string(out), promoteCanary) {
		t.Fatal("the credential canary reached a log line or reply")
	}
	h.rekey(t, []gateway.Account{gw})
	delete(reg.provs, "q")
	if _, err := bus.Publish(context.Background(), "fleet.topology", topologyReconciledKind, "test", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitRows(t, h, bus, "dq", 0)
	h.want(t, "dp", topology.BillingTierPaid, 1)
}

// waitRows waits up to 10 s for domain's evidence row count to reach n. A
// prune can lose one pass to a transient store error while the poll below
// reads the same database, so each unmet poll publishes another reconciled
// pass: the prune is idempotent and the wait ends on the real row count.
func waitRows(t *testing.T, h *promoteHarness, bus *events.Bus, domain string, n int) {
	t.Helper()
	tick, deadline := time.NewTicker(50*time.Millisecond), time.After(10*time.Second)
	defer tick.Stop()
	for {
		ev, err := h.topo.BillingEvidenceFor(context.Background(), topology.DomainID(domain))
		if err == nil && len(ev) == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s: %d rows (%v), want %d", domain, len(ev), err, n)
		case <-tick.C:
			if _, perr := bus.Publish(context.Background(), "fleet.topology", topologyReconciledKind, "test", []byte(`{}`)); perr != nil {
				t.Fatal(perr)
			}
		}
	}
}
