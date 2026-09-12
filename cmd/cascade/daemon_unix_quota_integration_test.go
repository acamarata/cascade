//go:build !windows && integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_FleetQuotaSnapshotReachableOverRealSocket proves
// fleet.quota.snapshot is reachable on the daemon's own socket, closing
// the same class of gap R-14.223 named for fleet.journal_show:
// internal/fleet/topology.RegisterHandlers had a test caller only before
// this ticket wired internal/daemon/quota_rpc.go into buildRPCServer's
// wireFleetAndNodeHandlers. It seeds a real domain/bucket through the
// SAME cascade.db file buildRPCServer's own RegisterFleetQuotaHandler
// opens (paths.DataDir()/cascade.db), dials fleet.quota.snapshot over a
// REAL unix socket with a REAL HTTP client (never calling the handler
// function in-process, which would prove nothing about whether the
// composition root actually registered it), and asserts the seeded
// bucket comes back with the expected minimum-confidence value.
func TestBuildRPCServer_FleetQuotaSnapshotReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	kvStore := storetest.NewMemStore()

	dir, err := os.MkdirTemp("", "quotae2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := fakeMemoryPaths{root: dir}
	sockPath := paths.SocketPath()

	// Seed one real domain/bucket through the SAME cascade.db file
	// buildRPCServer's own RegisterFleetQuotaHandler opens, so the
	// composition root's real handler (not a test-side substitute)
	// serves this exact row.
	seedQuotaFixture(t, paths)

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, paths, nil, kvStore)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "quota-e2e",
		"method":  topology.MethodFleetQuotaSnapshot,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s -> %d, want 200", rpc.RPCPath, resp.StatusCode)
	}

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result topology.QuotaSnapshot `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("fleet.quota.snapshot over the daemon socket returned an error: %d %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result.Domains) != 1 {
		t.Fatalf("Domains = %d, want 1", len(envelope.Result.Domains))
	}
	if got, want := envelope.Result.Domains[0].DomainID, topology.DomainID("acct-1:api"); got != want {
		t.Errorf("Domains[0].DomainID = %q, want %q", got, want)
	}
	if got, want := envelope.Result.Domains[0].Confidence, 0.4; got != want {
		t.Errorf("Domains[0].Confidence = %v, want %v (the minimum across the seeded dimensions)", got, want)
	}
}

// seedQuotaFixture writes one real QuotaDomain and its three api_project
// dimensions to paths.DataDir()/cascade.db, through the SAME
// topology.Store/topology.QuotaStore pair RegisterFleetQuotaHandler
// itself builds, before the daemon composition root ever opens the file.
func seedQuotaFixture(t *testing.T, paths runtime.PathProvider) {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open cascade.db: %v", err)
	}
	defer func() { _ = db.Close() }()

	clock := fixedQuotaSeedClock{t: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	if err := topology.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(paths.DataDir(), "backups")); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}

	store := topology.NewStore(db)
	if err := store.UpsertAccount(context.Background(), topology.Account{ID: "acct-1", Provider: "anthropic", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	domain := topology.QuotaDomain{ID: "acct-1:api", AccountRef: "acct-1", Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierDiscover}
	if err := store.UpsertQuotaDomain(context.Background(), domain); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}

	quotaStore := topology.NewQuotaStore(db)
	confidences := map[string]float64{topology.DimensionRPM: 0.9, topology.DimensionTPM: 0.4, topology.DimensionRPD: 0.8}
	for name, conf := range confidences {
		b := topology.Bucket{
			Name: name, Limit: 10, RemainingFraction: 0.5, Window: topology.BucketWindowDay,
			Source: topology.SourceCLIObservation, Confidence: conf, LimitScopeID: "scope:acct-1",
			CapacityObserved: topology.UnobservedCapacity,
		}
		if err := quotaStore.UpsertBucket(context.Background(), domain.ID, name, b); err != nil {
			t.Fatalf("UpsertBucket(%s): %v", name, err)
		}
	}
}

type fixedQuotaSeedClock struct{ t time.Time }

func (c fixedQuotaSeedClock) Now() time.Time { return c.t }
