package usage_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

type quotaClock struct{ t time.Time }

func (c *quotaClock) Now() time.Time { return c.t }

var quotaEpoch = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func openQuotaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "provider-usage.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migratedQuotaStore(t *testing.T, clk *quotaClock) (*usage.Manager, *usage.Reader) {
	t.Helper()
	db := openQuotaDB(t)
	if err := usage.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	m := usage.NewManager(db, clk)
	return m, usage.NewReader(m)
}

func TestQuotaStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	clk := &quotaClock{t: quotaEpoch}
	m, r := migratedQuotaStore(t, clk)
	obs := usage.QuotaObservation{Provider: "gw", Window: topology.BucketWindow5h, Used: 30, Limit: 120,
		UtilizationPct: usage.QuotaUnknown, ResetsAt: quotaEpoch.Add(time.Hour), ObservedAt: quotaEpoch}
	if err := m.RecordQuota(ctx, obs); err != nil {
		t.Fatalf("RecordQuota: %v", err)
	}
	obs.Used = 60 // the upsert replaces the row
	if err := m.RecordQuota(ctx, obs); err != nil {
		t.Fatalf("RecordQuota upsert: %v", err)
	}
	got, err := r.QuotaWindows(ctx, "gw")
	if err != nil || len(got) != 1 {
		t.Fatalf("QuotaWindows = %v, %v; want one window", got, err)
	}
	w := got[0]
	if w.Used != 60 || w.Limit != 120 || w.UtilizationPct != 50 || w.Stale || w.LastError != "" ||
		!w.ResetsAt.Equal(obs.ResetsAt) || !w.ObservedAt.Equal(quotaEpoch) || w.Window != topology.BucketWindow5h {
		t.Fatalf("round trip = %+v", w)
	}
	clk.t = quotaEpoch.Add(usage.QuotaStaleAfter + time.Second)
	got, _ = r.QuotaWindows(ctx, "gw")
	if !got[0].Stale || got[0].UtilizationPct != 50 {
		t.Fatalf("aged observation = %+v, want Stale with last values", got[0])
	}
	if err := m.RecordQuota(ctx, usage.QuotaObservation{Provider: "gw", Window: "bogus"}); err == nil {
		t.Fatal("an invalid window was accepted")
	}
}

func TestQuotaMigrationUpgrades(t *testing.T) {
	ctx := context.Background()
	clk := &quotaClock{t: quotaEpoch}
	db := openQuotaDB(t)
	prior := usage.MigrationSet()
	prior.SchemaVersion, prior.ReaderCeiling, prior.Steps = 6, 6, prior.Steps[:1]
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clk}
	if err := migrate.Apply(ctx, cfg, prior); err != nil {
		t.Fatalf("apply prior schema: %v", err)
	}
	m := usage.NewManager(db, clk)
	if err := m.IncrementUsage(ctx, usage.IncrementRequest{ProviderName: "gw", LaneName: "l", ModelName: "model-a", TokensIn: 7}); err != nil {
		t.Fatalf("IncrementUsage on prior schema: %v", err)
	}
	if err := usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("upgrade to %d: %v", usage.SchemaVersion, err)
	}
	if usage.SchemaVersion != 7 {
		t.Fatalf("SchemaVersion = %d, want 7", usage.SchemaVersion)
	}
	var tokens int64
	if err := db.QueryRowContext(ctx, `SELECT tokens_in FROM provider_usage WHERE provider_name='gw'`).Scan(&tokens); err != nil || tokens != 7 {
		t.Fatalf("existing usage row after upgrade: tokens=%d err=%v", tokens, err)
	}
	if err := m.RecordQuotaFailure(ctx, "gw", topology.BucketWindowWeek, "unavailable"); err != nil {
		t.Fatalf("quota table missing after upgrade: %v", err)
	}
	got, err := usage.NewReader(m).QuotaWindows(ctx, "gw")
	if err != nil || len(got) != 1 || !got[0].Stale || got[0].UtilizationPct != usage.QuotaUnknown || got[0].Used != usage.QuotaUnknown {
		t.Fatalf("never-read failed window = %+v, %v; want stale and unknown", got, err)
	}
}

func TestQuotaReplaysCapturedSource(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "gateway_quota", "usage-source.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var tbl map[string]any
	if err := toml.Unmarshal(raw, &tbl); err != nil {
		t.Fatal(err)
	}
	accts, err := gateway.ParseAccounts(map[string]any{"gw": map[string]any{
		"base_url": "https://gateway.invalid", "key_env": "GATEWAY_TOKEN", "usage": tbl}})
	if err != nil || len(accts) != 1 {
		t.Fatalf("ParseAccounts = %v, %v", accts, err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "gateway_quota", "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := gateway.DecodeEndpoint(accts[0].Usage, body, quotaEpoch)
	if err != nil {
		t.Fatalf("DecodeEndpoint: %v", err)
	}
	want := usage.QuotaObservation{Window: topology.BucketWindow5h, Used: 420, Limit: 1000, UtilizationPct: 42.5,
		ResetsAt: time.Date(2026, 9, 6, 15, 0, 0, 0, time.UTC), ObservedAt: quotaEpoch}
	if obs != want {
		t.Fatalf("decoded = %+v, want %+v", obs, want)
	}
	obs.Provider = "gw"
	clk := &quotaClock{t: quotaEpoch}
	m, r := migratedQuotaStore(t, clk)
	if err := m.RecordQuota(context.Background(), obs); err != nil {
		t.Fatal(err)
	}
	got, err := r.QuotaWindows(context.Background(), "gw")
	if err != nil || len(got) != 1 || got[0].UtilizationPct != 42.5 || got[0].Stale {
		t.Fatalf("stored replay = %+v, %v", got, err)
	}
}

func TestQuotaRecordRefusesAndDefaults(t *testing.T) {
	ctx := context.Background()
	clk := &quotaClock{t: quotaEpoch}
	m, r := migratedQuotaStore(t, clk)
	for name, err := range map[string]error{
		"no provider":    m.RecordQuota(ctx, usage.QuotaObservation{Window: topology.BucketWindow5h}),
		"bad window":     m.RecordQuota(ctx, usage.QuotaObservation{Provider: "gw", Window: "fortnight"}),
		"failure no cls": m.RecordQuotaFailure(ctx, "gw", topology.BucketWindow5h, ""),
		"failure no win": m.RecordQuotaFailure(ctx, "gw", "fortnight", "decode"),
	} {
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// A never-read window that fails gets every figure unknown, stale.
	if err := m.RecordQuotaFailure(ctx, "gw", topology.BucketWindowWeek, "unavailable"); err != nil {
		t.Fatalf("RecordQuotaFailure: %v", err)
	}
	// Zero ObservedAt takes the store clock; zero ResetsAt stays unknown.
	if err := m.RecordQuota(ctx, usage.QuotaObservation{Provider: "gw", Window: topology.BucketWindow5h,
		Used: usage.QuotaUnknown, Limit: 0, UtilizationPct: usage.QuotaUnknown}); err != nil {
		t.Fatalf("RecordQuota: %v", err)
	}
	got, err := r.QuotaWindows(ctx, "gw")
	if err != nil || len(got) != 2 {
		t.Fatalf("QuotaWindows = %+v, %v", got, err)
	}
	for _, o := range got {
		switch o.Window {
		case topology.BucketWindow5h:
			if !o.ObservedAt.Equal(quotaEpoch) || !o.ResetsAt.IsZero() || o.UtilizationPct != -1 || o.Stale {
				t.Fatalf("5h = %+v", o)
			}
		case topology.BucketWindowWeek:
			if o.Used != -1 || o.Limit != -1 || o.UtilizationPct != -1 || !o.Stale || o.LastError != "unavailable" {
				t.Fatalf("week = %+v", o)
			}
		case topology.BucketWindowDay, topology.BucketWindowMonth, topology.BucketWindowRolling:
			t.Fatalf("unexpected window %+v", o)
		}
	}
}

func TestQuotaUtilizationClamps(t *testing.T) {
	for _, tc := range []struct {
		used, limit int64
		pct, want   float64
	}{
		{30, 120, -1, 25}, {500, 100, -1, 100}, {-1, 100, -1, -1}, {5, 0, -1, -1}, {0, 0, 140, 100}, {0, 0, 7.5, 7.5},
	} {
		o := usage.QuotaObservation{Used: tc.used, Limit: tc.limit, UtilizationPct: tc.pct}
		if got := o.Utilization(); got != tc.want {
			t.Fatalf("Utilization(%+v) = %v, want %v", tc, got, tc.want)
		}
	}
}
