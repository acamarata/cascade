//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

const quotaBody = `{"windows":{"five_hour":{"used":420,"limit":1000,"utilization":42.5}}}`

var quotaTestNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

type quotaFakeDoer struct {
	calls atomic.Int32
	auth  atomic.Value
}

func (d *quotaFakeDoer) do(_ context.Context, _, header, value string) (int, []byte, error) {
	d.calls.Add(1)
	d.auth.Store(header + ": " + value)
	return 200, []byte(quotaBody), nil
}

func quotaAccount(name, kind string) gateway.Account {
	return gateway.Account{Name: name, BaseURL: "https://gateway.invalid", Header: gateway.HeaderBearer,
		Usage: gateway.UsageSource{Kind: kind, Window: "5h", Path: "/v1/usage", UsedPointer: "/windows/five_hour/used",
			LimitPointer: "/windows/five_hour/limit", UtilizationPointer: "/windows/five_hour/utilization"}}
}

func quotaTestStore(t *testing.T) (*usage.Manager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "provider-usage.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clk := runtime.NewFixedClock(quotaTestNow)
	if err := usage.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatal(err)
	}
	return usage.NewManager(db, clk), path
}

func quotaTestPoll(store *usage.Manager, reg *egress.Registry, doer *quotaFakeDoer, token string) quotaPoll {
	return quotaPoll{Egress: reg, Doer: doer.do, Store: store, Clock: runtime.NewFixedClock(quotaTestNow),
		Token:     func(context.Context, gateway.Account) ([]byte, error) { return []byte(token), nil },
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }}
}

func TestQuotaPollRequiresEgressClass(t *testing.T) {
	store, _ := quotaTestStore(t)
	disabled := egress.NewRegistry()
	disabled.MustRegister(egress.EgressClassProviderUsage, egress.InterceptConfig{Enabled: false, Owner: "test"})
	for name, reg := range map[string]*egress.Registry{"unregistered": egress.NewRegistry(), "disabled": disabled, "nil": nil} {
		doer := &quotaFakeDoer{}
		quotaTestPoll(store, reg, doer, "t").pollOnce(context.Background(), quotaAccount("gw", gateway.UsageSourceEndpoint))
		if n := doer.calls.Load(); n != 0 {
			t.Fatalf("%s class: %d requests, want 0", name, n)
		}
	}
	obs, err := usage.NewReader(store).QuotaWindows(context.Background(), "gw")
	if err != nil || len(obs) != 1 || !obs[0].Stale || obs[0].LastError != "egress-refused" {
		t.Fatalf("refused poll = %+v, %v; want a stale egress-refused window", obs, err)
	}
	doer := &quotaFakeDoer{}
	quotaTestPoll(store, egress.DefaultRegistry(), doer, "t").pollOnce(context.Background(), quotaAccount("gw", gateway.UsageSourceEndpoint))
	if doer.calls.Load() != 1 {
		t.Fatalf("admitted poll made %d requests, want 1", doer.calls.Load())
	}
}

func TestGatewayQuotaWiring(t *testing.T) {
	store, _ := quotaTestStore(t)
	ticks := make(chan time.Time)
	var interval time.Duration
	doer := &quotaFakeDoer{}
	p := quotaTestPoll(store, egress.DefaultRegistry(), doer, "t")
	p.NewTicker = func(d time.Duration) (<-chan time.Time, func()) { interval = d; return ticks, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var started []string
	goSup := func(ctx context.Context, name, _ string, fn func(context.Context) error) {
		started = append(started, name)
		wg.Add(1)
		go func() { defer wg.Done(); _ = fn(ctx) }()
	}
	comp := capacity.NewCompositor(runtime.NewFixedClock(quotaTestNow), time.Hour, "")
	accounts := []gateway.Account{quotaAccount("gw", gateway.UsageSourceEndpoint), quotaAccount("hd", gateway.UsageSourceHeaders),
		{Name: "plain", BaseURL: "https://gateway.invalid"}}
	restore := startGatewayQuota(ctx, comp, goSup, accounts, p)
	defer restore()
	if len(started) != 1 || started[0] != "gateway-quota/gw" {
		t.Fatalf("pollers = %v, want one for the endpoint account", started)
	}
	ticks <- quotaTestNow // the first poll ran; this tick starts the second
	ticks <- quotaTestNow
	cancel()
	wg.Wait()
	if interval != 60*time.Second || doer.calls.Load() < 2 {
		t.Fatalf("interval %v, %d polls; want 60s and a poll per tick", interval, doer.calls.Load())
	}
	restore()
	src := &fakeQuotaProviders{}
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	slot := comp.Snapshot().Providers["gw"]
	if len(slot.Quota) != 1 || slot.Quota[0].UtilizationPct != 42.5 {
		t.Fatalf("compositor quota = %+v, want the polled window (SetQuotaSource)", slot.Quota)
	}
}

type fakeQuotaProviders struct{}

func (fakeQuotaProviders) ListProviders(context.Context) ([]registry.ProviderRecord, error) {
	return []registry.ProviderRecord{{Name: "gw"}}, nil
}

func (fakeQuotaProviders) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	return []registry.LaneRecord{{ProviderName: "gw", Capacity: capacity.BucketInteractiveUsage, State: registry.LaneStateAvailable}}, nil
}

func TestGatewayQuotaWiringFeedsWidget(t *testing.T) {
	ww := newWidgetWire(t)
	ww.w.Deps.Fleet = nil
	if err := wireGatewayQuota(ww.w); err != nil {
		t.Fatalf("wireGatewayQuota: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(ww.dataDir, providerUsageDBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := usage.NewManager(db, ww.clock)
	p := quotaTestPoll(store, egress.DefaultRegistry(), &quotaFakeDoer{}, "t")
	p.Clock = ww.clock
	p.pollOnce(context.Background(), quotaAccount("gw", gateway.UsageSourceEndpoint))
	ww.seedProvider("gw", registry.DriverAnthropic, registry.LaneStateAvailable, "")
	row := rowByRef(t, ww.snapshot(), "gw")
	if row.FiveHour.UtilizationPct == nil || *row.FiveHour.UtilizationPct != 42.5 {
		t.Fatalf("status.widget five_hour = %v, want 42.5", row.FiveHour.UtilizationPct)
	}
	if err := store.RecordQuotaFailure(context.Background(), "gw", "5h", "unavailable"); err != nil {
		t.Fatal(err)
	}
	if row := rowByRef(t, ww.snapshot(), "gw"); row.FiveHour.UtilizationPct != nil {
		t.Fatalf("failed read five_hour = %v, want null", *row.FiveHour.UtilizationPct)
	}
}

func TestQuotaCanaryAbsent(t *testing.T) {
	store, path := quotaTestStore(t)
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	canary := "canary-" + hex.EncodeToString(raw)
	var logs bytes.Buffer
	doer := &quotaFakeDoer{}
	p := quotaTestPoll(store, egress.DefaultRegistry(), doer, canary)
	p.pollOnce(context.Background(), quotaAccount("gw", gateway.UsageSourceEndpoint))
	slog.New(slog.NewTextHandler(&logs, nil)).Info("poll", "class", "done")
	if got, _ := doer.auth.Load().(string); got != "Authorization: Bearer "+canary {
		t.Fatal("the poll did not present the injected token")
	}
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if err == nil && bytes.Contains(b, []byte(canary)) {
			t.Fatalf("canary found in %s", filepath.Base(f))
		}
	}
	obs, _ := usage.NewReader(store).QuotaWindows(context.Background(), "gw")
	for _, o := range obs {
		if strings.Contains(o.LastError, canary) {
			t.Fatal("canary found in a stored error")
		}
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatal("canary found in logs")
	}
}
