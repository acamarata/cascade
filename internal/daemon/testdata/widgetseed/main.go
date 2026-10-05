// Command widgetseed is a TEST-FIXTURE program, never part of the product: it
// writes provider records and lanes straight into a providers.db so the
// status widget has rows to show, with no credential and no vault. The Swift
// real-daemon tests and every QA leg seed with it.
//
// Usage:
//
//	widgetseed seed --data-dir DIR
//	widgetseed lane --data-dir DIR --name NAME --state STATE [--reset-seconds N]
//
// seed stores five key-authenticated providers (an available one, a pooled
// auth-required one, an exhausted one with a reset two hours out, one with no
// lane, and an email-named one) through registry.AddProvider and
// registry.UpsertLane. lane moves one provider's lane to STATE (available,
// constrained, exhausted, auth-required or unknown), with a reset estimate
// N seconds from now when --reset-seconds is given: the way a QA leg
// "flips a lane" and expects a status.widget_changed frame within one tick.
//
// Every record it writes is Auth=key, so no OAuth or browser flow can start
// against one. AuthRef is a vault-key NAME, never a value, and nothing is
// stored under it; the program imports no secrets or vault package (checked
// by `go list -deps`), and it never reads the environment or the network.
//
// Provenance: written for P1-WID-08; the rows it produces are the same five
// shapes as internal/daemon/testdata/fixture_status_widget_rpc.json.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// seedSpec is one provider the seed command writes.
type seedSpec struct {
	name  string
	state registry.LaneState // "" means no lane
	pool  string
	reset time.Duration
}

var seedSet = []seedSpec{
	{name: "widget-avail", state: registry.LaneStateAvailable},
	{name: "widget-pool", state: registry.LaneStateAuthRequired, pool: "pool-1"},
	{name: "widget-limit", state: registry.LaneStateExhausted, reset: 2 * time.Hour},
	{name: "widget-nolane"},
	{name: "user@example.com", state: registry.LaneStateAvailable},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "widgetseed:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || (args[0] != "seed" && args[0] != "lane") {
		return fmt.Errorf("usage: widgetseed seed|lane --data-dir DIR [lane flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "directory holding providers.db")
	name := fs.String("name", "", "provider whose lane to move (lane)")
	state := fs.String("state", "", "lane state (lane)")
	resetSeconds := fs.Int("reset-seconds", 0, "reset estimate, seconds from now (lane)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dataDir == "" {
		return fmt.Errorf("--data-dir is required")
	}
	clock := runtime.NewSystemClock()
	reg, closeDB, err := open(*dataDir, clock)
	if err != nil {
		return err
	}
	defer closeDB()
	if args[0] == "seed" {
		return seed(context.Background(), reg, clock)
	}
	return moveLane(context.Background(), reg, clock, *name, registry.LaneState(*state), time.Duration(*resetSeconds)*time.Second)
}

// open opens and migrates providers.db under dir.
func open(dir string, clock runtime.Clock) (*registry.Registry, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "providers.db"))
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return registry.NewRegistry(db, clock), func() { _ = db.Close() }, nil
}

// seed writes seedSet; a provider that already exists keeps its record and
// has its lane rewritten, so seed can be run twice.
func seed(ctx context.Context, reg *registry.Registry, clock runtime.Clock) error {
	for _, s := range seedSet {
		rec := registry.ProviderRecord{
			Name: s.name, Driver: registry.DriverOpenAICompat, BaseURL: "https://example.invalid/v1",
			Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef("provider." + vaultSafe(s.name) + ".key"),
			AccountKind: registry.AccountPersonal, Tier: registry.TierMid, HealthStatus: registry.HealthUnknown,
		}
		if err := reg.AddProvider(ctx, rec); err != nil && !cascade.HasKind(err, cascade.KindConflict) {
			return fmt.Errorf("add %q: %w", s.name, err)
		}
		if s.state == "" {
			continue
		}
		lane := registry.LaneRecord{LaneName: s.name, ProviderName: s.name, Weight: 1, PoolMembership: s.pool,
			Capacity: registry.CapacityAPICredit, State: s.state}
		if s.reset > 0 {
			lane.ResetEstimate = clock.Now().Add(s.reset)
		}
		if err := reg.UpsertLane(ctx, lane); err != nil {
			return fmt.Errorf("lane %q: %w", s.name, err)
		}
	}
	return nil
}

// vaultSafe maps a provider name onto the vault-name charset for AuthRef.
func vaultSafe(name string) string {
	out := []byte(name)
	for i, c := range out {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			out[i] = '-'
		}
	}
	return string(out)
}

// moveLane sets name's lane to state (creating the lane when the provider
// has none), with a reset estimate reset from now when reset > 0.
func moveLane(ctx context.Context, reg *registry.Registry, clock runtime.Clock, name string, state registry.LaneState, reset time.Duration) error {
	if name == "" || !state.Valid() {
		return fmt.Errorf("lane needs --name and a valid --state (got %q, %q)", name, state)
	}
	if _, err := reg.GetProvider(ctx, name); err != nil {
		return fmt.Errorf("provider %q: %w", name, err)
	}
	lane := registry.LaneRecord{LaneName: name, ProviderName: name, Weight: 1, Capacity: registry.CapacityAPICredit}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		return err
	}
	for _, l := range lanes {
		if l.ProviderName == name {
			lane = l
			break
		}
	}
	lane.State, lane.ResetEstimate = state, time.Time{}
	if reset > 0 {
		lane.ResetEstimate = clock.Now().Add(reset)
	}
	return reg.UpsertLane(ctx, lane)
}
