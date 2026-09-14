// Purpose: unit coverage for `cascade fleet usage` (P1-E18-W4-S40-T5): the
// shared test fixture loader/seeder, the fixedDataDirPaths test double,
// the CLI driver helper, and the table/--json acceptance tests. Filter,
// headless, behavioral-script, parser, and fuzz coverage live in
// fleet_usage_filters_test.go (300-line file cap, LANE-RULES §8).
//
// SPORT: cmd.cascade.fleet-usage/TEST (P1-E18-W4-S40-T5).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// fixedDataDirPaths is a minimal runtime.PathProvider whose DataDir() is
// exactly the caller-supplied directory, unlike fakeDaemonPaths (which
// derives DataDir() as root/data) — used here because
// seedFleetUsageFixture and openFleetUsageReader must agree on the exact
// same provider-usage.db location without an extra join.
type fixedDataDirPaths struct{ dataDir string }

func (p fixedDataDirPaths) Root() string       { return filepath.Dir(p.dataDir) }
func (p fixedDataDirPaths) ConfigPath() string { return filepath.Join(p.Root(), "config.toml") }
func (p fixedDataDirPaths) SocketPath() string { return filepath.Join(p.Root(), "daemon.sock") }
func (p fixedDataDirPaths) DataDir() string    { return p.dataDir }
func (p fixedDataDirPaths) LogDir() string     { return filepath.Join(p.Root(), "logs") }
func (p fixedDataDirPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.dataDir, "storage", string(prof))
}

// usageFixtureRecord/usageFixture mirror internal/fleet/testdata/
// usage-fixture.json's shape: one IncrementUsage call per record, dated
// via a frozen clock so the resulting provider_usage bucket matches the
// fixture's own "date" field exactly.
type usageFixtureRecord struct {
	Provider     string `json:"provider"`
	Lane         string `json:"lane"`
	Model        string `json:"model"`
	Date         string `json:"date"`
	TokensIn     int64  `json:"tokens_in"`
	TokensOut    int64  `json:"tokens_out"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
}

type usageFixture struct {
	Records []usageFixtureRecord `json:"records"`
}

// loadUsageFixture reads the shared fixture from its files_scope path,
// relative to this package's own directory.
func loadUsageFixture(t *testing.T) usageFixture {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "fleet", "testdata", "usage-fixture.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage fixture %s: %v", path, err)
	}
	var f usageFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse usage fixture: %v", err)
	}
	if len(f.Records) < 5 {
		t.Fatalf("usage fixture has %d records, want >=5 (>=2 providers/3 models/2 sessions)", len(f.Records))
	}
	return f
}

// seedFleetUsageFixture writes every fixture record into dataDir's
// provider-usage.db via the real production write path (usage.Manager.
// IncrementUsage over openMigratedDB, both already used by
// runFleetUsage/openFleetUsageReader) — never a hand-rolled INSERT, so
// the seeded state can only ever be what the domain itself would produce.
func seedFleetUsageFixture(t *testing.T, dataDir string, fixture usageFixture) {
	t.Helper()
	for _, rec := range fixture.Records {
		date, err := time.Parse("2006-01-02", rec.Date)
		if err != nil {
			t.Fatalf("parse fixture date %q: %v", rec.Date, err)
		}
		clk := testkit.NewFrozenClock(date)
		db, err := openMigratedDB(context.Background(), filepath.Join(dataDir, providerUsageDBFile),
			func(ctx context.Context, db *sql.DB) error {
				return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "")
			})
		if err != nil {
			t.Fatalf("open usage db: %v", err)
		}
		mgr := usage.NewManager(db, clk)
		if err := mgr.IncrementUsage(context.Background(), usage.IncrementRequest{
			ProviderName: rec.Provider, LaneName: rec.Lane, ModelName: rec.Model,
			TokensIn: rec.TokensIn, TokensOut: rec.TokensOut, CostMicroUSD: rec.CostMicroUSD,
		}); err != nil {
			t.Fatalf("seed fixture record %+v: %v", rec, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close usage db: %v", err)
		}
	}
}

// newTestFleetUsageRoot builds a minimal root carrying the same
// persistent global flags root.go registers (json/quiet/verbose/
// no-color), so fleetSessionsOutputWriter's flag reads resolve exactly as
// they do under the real `cascade` binary.
func newTestFleetUsageRoot(deps fleetSessionsDeps) *cobra.Command {
	root := &cobra.Command{Use: "cascade"}
	flags := root.PersistentFlags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")
	root.AddCommand(newFleetUsageCmd(deps))
	return root
}

// runFleetUsageCLI drives `cascade fleet usage <args...>` end to end and
// returns combined stdout and the Execute() error.
func runFleetUsageCLI(deps fleetSessionsDeps, args ...string) (string, error) {
	root := newTestFleetUsageRoot(deps)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"usage"}, args...))
	err := root.Execute()
	return out.String(), err
}

// TestFleetUsage: the table view over the seeded fixture matches the
// aggregated expectation exactly (row count, token sums, cost sums).
func TestFleetUsage(t *testing.T) {
	dataDir := t.TempDir()
	fixture := loadUsageFixture(t)
	seedFleetUsageFixture(t, dataDir, fixture)
	deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}

	out, err := runFleetUsageCLI(deps)
	if err != nil {
		t.Fatalf("fleet usage: %v (output: %s)", err, out)
	}
	for _, want := range []string{
		"anthropic", "claude-sonnet-5", "requests=2", "tokens_in=1500", "tokens_out=300", "cost_micro_usd=7500",
		"claude-haiku-5", "requests=1", "tokens_in=3000",
		"openai", "gpt-5-codex", "requests=2", "tokens_in=4200", "cost_micro_usd=16800",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet usage table output missing %q; got:\n%s", want, out)
		}
	}
}

// TestFleetUsageJSON: --json emits a versioned envelope parseable by
// encoding/json, carrying the same aggregated rows.
func TestFleetUsageJSON(t *testing.T) {
	dataDir := t.TempDir()
	fixture := loadUsageFixture(t)
	seedFleetUsageFixture(t, dataDir, fixture)
	deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}

	out, err := runFleetUsageCLI(deps, "--json")
	if err != nil {
		t.Fatalf("fleet usage --json: %v (output: %s)", err, out)
	}
	var envelope struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Data    struct {
			Rows []fleetUsageRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err != nil {
		t.Fatalf("fleet usage --json: not valid JSON: %v\noutput: %s", err, out)
	}
	if envelope.Version != 1 || !envelope.OK {
		t.Fatalf("fleet usage --json: envelope = %+v, want version=1 ok=true", envelope)
	}
	if len(envelope.Data.Rows) != 3 {
		t.Fatalf("fleet usage --json: got %d rows, want 3 (2 anthropic models + 1 openai model)", len(envelope.Data.Rows))
	}
}
