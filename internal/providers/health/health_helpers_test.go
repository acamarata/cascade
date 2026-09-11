package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// Purpose: shared test setup helpers for this package's *_test.go files
// (split out of health_test.go to stay under the repo's 300-line file
// cap). Nothing here is exported outside the package.

// openTestDB opens a fresh in-memory modernc SQLite database for one test.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestRegistry opens an in-memory DB, applies the registry migration,
// and returns a ready-to-use *registry.Registry stamped by clk.
func newTestRegistry(t *testing.T, clk registry.Clock) *registry.Registry {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return registry.NewRegistry(db, clk)
}

// newTestManager wires a Manager over a fresh registry and a real
// events.Bus backed by storetest.NewMemStore(), stamped by clk. threshold
// 0 uses DefaultEvictionThreshold.
func newTestManager(t *testing.T, clk *testkit.FrozenClock, threshold int) (*Manager, *registry.Registry, *events.Bus) {
	t.Helper()
	reg := newTestRegistry(t, clk)
	bus := events.New(storetest.NewMemStore(), clk)
	return NewManager(reg, bus, clk, threshold), reg, bus
}

func sampleProvider(name string) registry.ProviderRecord {
	return registry.ProviderRecord{
		Name:         name,
		Driver:       registry.DriverAnthropic,
		BaseURL:      "https://api.anthropic.com",
		Auth:         registry.AuthKey,
		AuthRef:      registry.VaultKeyRef("provider." + name + ".key"),
		KnownModels:  []string{"claude-3-5-sonnet-20241022"},
		AccountKind:  registry.AccountPersonal,
		Tier:         registry.TierStrong,
		HealthStatus: registry.HealthUnknown,
	}
}

func seedProvider(t *testing.T, reg *registry.Registry, name string) {
	t.Helper()
	if err := reg.UpsertProvider(context.Background(), sampleProvider(name)); err != nil {
		t.Fatalf("seed provider %q: %v", name, err)
	}
}

func subscribeEvents(t *testing.T, bus *events.Bus, namespace, cursor string) <-chan events.Event {
	t.Helper()
	sub, err := bus.Subscribe(context.Background(), namespace, cursor, 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return sub.Events
}

// assertGolden compares got (JSON-marshaled) against the fixture at path,
// updating the fixture only when CASCADE_UPDATE_GOLDEN=1 is set (never in
// CI).
func assertGolden(t *testing.T, path string, got any) {
	t.Helper()
	gotBytes, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	if os.Getenv("CASCADE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, append(gotBytes, '\n'), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	var wantVal, gotVal any
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("decode golden %s: %v", path, err)
	}
	if err := json.Unmarshal(gotBytes, &gotVal); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	wantJSON, _ := json.Marshal(wantVal)
	gotJSON, _ := json.Marshal(gotVal)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("golden mismatch for %s:\n want=%s\n got=%s", path, wantJSON, gotJSON)
	}
}
