// Purpose: the gateway recovery and doctor acceptance tests (P1-GWY-09): a
//
//	degraded gateway provider recovers only on an authenticated 2xx, never
//	because its base URL answers below 500, and the doctor's
//	provider_health check reports it while degraded.
//
// Inputs: a fake intake.Doer routing by path, a canary token from an
//
//	injected gateway.Source, a temp providers.db.
//
// Outputs: none.
// Constraints: no network, no keychain, no operator HOME; the host is the
//
//	reserved .invalid name.
//
// SPORT: cmd/cascade provider_health_probe/ADD (P1-GWY-09).

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/health"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	probeGatewayBase = "https://gateway.invalid"
	probeCanary      = "probe-canary-Zq4Zq4Zq4Zq4"
)

// probeEpoch is the frozen instant the probe tests' clocks start at.
var probeEpoch = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// pathDoer answers POST /v1/messages with messagesStatus and records what it
// was sent; any other path is the root answer.
type pathDoer struct {
	mu             sync.Mutex
	messagesStatus int
	rootStatus     int
	posts          []intake.HTTPRequest
}

func (d *pathDoer) Do(_ context.Context, req intake.HTTPRequest) (intake.HTTPResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.HasSuffix(req.URL, "/v1/messages") {
		d.posts = append(d.posts, req)
		return intake.HTTPResponse{Status: d.messagesStatus}, nil
	}
	return intake.HTTPResponse{Status: d.rootStatus}, nil
}

type probeVault struct{}

func (probeVault) List(context.Context) ([]string, error)      { return nil, nil }
func (probeVault) Get(context.Context, string) ([]byte, error) { return nil, nil }

func probeAccount() gateway.Account {
	return gateway.Account{Name: "acct", BaseURL: probeGatewayBase, Header: gateway.HeaderBearer, Models: []string{"model-a"},
		Credential: gateway.Credential{Kind: gateway.CredentialEnv, EnvVar: "PROBE_TOKEN"}}
}

func probeConfig() gatewayProbeConfig {
	return gatewayProbeConfig{Accounts: []gateway.Account{probeAccount()},
		Source: gateway.Source{Getenv: func(k string) string {
			if k == "PROBE_TOKEN" {
				return probeCanary
			}
			return ""
		}}}
}

func probeGatewayRecord(h registry.HealthStatus) registry.ProviderRecord {
	return registry.ProviderRecord{Name: "gw1", Driver: registry.DriverAnthropic, BaseURL: probeGatewayBase,
		Auth: registry.AuthGateway, AuthRef: "gateway:acct", KnownModels: []string{"model-a"},
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid, HealthStatus: h}
}

func openProbeRegistry(t *testing.T, dir string, clock *testkit.FrozenClock) *registry.Registry {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openMigratedDB(context.Background(), filepath.Join(dir, providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		t.Fatalf("openMigratedDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry.NewRegistry(db, clock)
}

func TestGatewayRecoveryNeedsAuthenticatedSuccess(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(probeEpoch)
	reg := openProbeRegistry(t, t.TempDir(), clock)
	if err := reg.UpsertProvider(ctx, probeGatewayRecord(registry.HealthUnknown)); err != nil {
		t.Fatal(err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), probeVault{}, detector)
	if err != nil {
		t.Fatal(err)
	}
	doer := &pathDoer{messagesStatus: 500, rootStatus: 404}
	prober := newRoutedProber(providerDeps{Doer: doer, HealthHTTPDoer: fakeHTTPDoer{status: 404}}, probeConfig())
	mgr := health.NewManager(reg, events.New(storetest.NewMemStore(), clock), clock, 0).WithEgress(engine, prober)
	if err := mgr.DemoteProvider(ctx, "gw1", provider.ReasonTransientUpstream); err != nil {
		t.Fatal(err)
	}

	// The stub-passing input: the reachability prober calls this 404 root healthy.
	if res, _ := prober.reach.Probe(ctx, probeGatewayRecord("")); !res.Success {
		t.Fatalf("precondition: reachability must pass a 404 root, got %+v", res)
	}
	if ok, err := mgr.RecoverProbe(ctx, "gw1"); ok || err != nil {
		t.Fatalf("RecoverProbe with /v1/messages 500 = (%v, %v), want not recovered", ok, err)
	}
	if st, _ := mgr.GetHealth(ctx, "gw1"); st != provider.HealthDegraded {
		t.Fatalf("health = %q after a failing probe, want degraded", st)
	}
	doer.messagesStatus = 404
	if ok, err := mgr.RecoverProbe(ctx, "gw1"); ok || err != nil {
		t.Fatalf("RecoverProbe with /v1/messages 404 = (%v, %v), want not recovered", ok, err)
	}
	doer.messagesStatus = 200
	if ok, err := mgr.RecoverProbe(ctx, "gw1"); !ok || err != nil {
		t.Fatalf("RecoverProbe with /v1/messages 200 = (%v, %v), want recovered", ok, err)
	}
	if st, _ := mgr.GetHealth(ctx, "gw1"); st != provider.HealthHealthy {
		t.Fatalf("health = %q after a 2xx, want healthy", st)
	}
	if len(doer.posts) != 3 || doer.posts[2].Headers["authorization"] != "Bearer "+probeCanary {
		t.Fatalf("posts = %d, want 3 carrying the bearer canary", len(doer.posts))
	}
	if !strings.Contains(string(doer.posts[2].Body), `"max_tokens":1`) {
		t.Fatalf("probe body %q is not a max_tokens-1 request", doer.posts[2].Body)
	}
}

func TestGatewayProbeRefusesWithoutAdmission(t *testing.T) {
	ctx := context.Background()
	doer := &pathDoer{messagesStatus: 200}
	for name, cfg := range map[string]gatewayProbeConfig{
		"no admitted account": {},
		"token unavailable":   {Accounts: probeConfig().Accounts, Source: gateway.Source{Getenv: func(string) string { return "" }}},
	} {
		res, err := gatewayProber{doer: doer, cfg: cfg}.Probe(ctx, probeGatewayRecord(""))
		if err != nil || res.Success || strings.Contains(res.TransportErr, probeCanary) {
			t.Fatalf("%s: result %+v err %v, want a content-free not-healthy", name, res, err)
		}
	}
	other := probeGatewayRecord("")
	other.BaseURL = "https://other.invalid"
	if res, _ := (gatewayProber{doer: doer, cfg: probeConfig()}).Probe(ctx, other); res.Success {
		t.Fatalf("a base URL different from the admitted one probed healthy: %+v", res)
	}
	if len(doer.posts) != 0 {
		t.Fatalf("%d requests sent before admission", len(doer.posts))
	}
}

func TestDoctorReportsDegradedGateway(t *testing.T) {
	ctx := context.Background()
	paths := doctorTestPaths(t)
	reg := openProbeRegistry(t, paths.DataDir(), testkit.NewFrozenClock(probeEpoch))
	if err := reg.UpsertProvider(ctx, probeGatewayRecord(registry.HealthDegraded)); err != nil {
		t.Fatal(err)
	}
	res, err := doctor.NewProviderHealthCheck(providerHealthSourceFor(paths)).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "gw1") {
		t.Fatalf("doctor = %+v, want an error naming the degraded gateway provider gw1", res)
	}
}
