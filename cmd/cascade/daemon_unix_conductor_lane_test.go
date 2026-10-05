//go:build !windows

// Purpose: proves the lane-outcome recorder is reachable from the registry
// the daemon's resolver really reads. wireConductorExecute builds its
// transport inline and its credentials through custody selection, so it is
// not constructed here; the reach is shown instead by (a) a compile-time
// assertion that the *registry.Registry value wireConductorExecute passes
// satisfies the widened lookup, and (b) a resolver built over a migrated
// providers.db opened the way the daemon opens it.
// Inputs: the live-captured openai 401 fixture under internal/providers/
// dispatch/testdata/lane_outcome (replayed by a fake Transport).
// Outputs: none.
// Constraints: no network, no vault, no keychain: the credential source is
// a fake and HOME is a temp dir. The helpers here are shared with
// provider_reauth_lane_test.go.
// SPORT: cmd/cascade daemon (CHANGE, P1-WID-11).

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The registry value wireConductorExecute hands to NewResolver satisfies the
// widened lookup, so the recorder's UpsertLane is the registry's own.
var _ providerdispatch.RegistryLookup = (*providerregistry.Registry)(nil)

// fixedTransport answers every send with one status and body, and counts sends.
type fixedTransport struct {
	status int
	body   []byte
	calls  int
}

func (f *fixedTransport) Send(context.Context, string, string, map[string]string, []byte) (int, map[string][]string, io.ReadCloser, error) {
	f.calls++
	return f.status, nil, io.NopCloser(bytes.NewReader(f.body)), nil
}

// mapCredentials is a CredentialSource over a fixed map.
type mapCredentials map[string]string

func (m mapCredentials) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := m[ref]; ok {
		return v, nil
	}
	return "", cascade.Newf(cascade.KindNotFound, "test: no credential %q", ref)
}

// openLaneRegistry opens providers.db under dir the way the daemon does.
func openLaneRegistry(t *testing.T, dir string, clock runtime.Clock) (*providerregistry.Registry, func()) {
	t.Helper()
	db, err := openMigratedDB(context.Background(), filepath.Join(dir, providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		t.Fatalf("openMigratedDB: %v", err)
	}
	return providerregistry.NewRegistry(db, clock), func() { _ = db.Close() }
}

// laneState returns the named lane's stored state and reset estimate.
func laneState(t *testing.T, reg *providerregistry.Registry, lane string) (providerregistry.LaneState, time.Time) {
	t.Helper()
	lanes, err := reg.ListLanes(context.Background())
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	for _, l := range lanes {
		if l.LaneName == lane {
			return l.State, l.ResetEstimate
		}
	}
	t.Fatalf("lane %q is not stored (%d lanes)", lane, len(lanes))
	return "", time.Time{}
}

// callThroughResolver resolves lane on reg with a fake credential and the
// given transport, runs one Chat, and returns its error.
func callThroughResolver(t *testing.T, reg *providerregistry.Registry, clock runtime.Clock, tr *fixedTransport, lane, ref string) error {
	t.Helper()
	res, err := providerdispatch.NewResolver(reg, mapCredentials{ref: "cred-value"}, clock, tr)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	p, err := res.Resolve(context.Background(), provider.Selection{LaneID: lane, Model: "test-model"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	return err
}

// openai401 returns the live-captured openai 401 body from the dispatch
// fixture directory.
func openai401(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "providers", "dispatch", "testdata", "lane_outcome", "openai_401_live.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil || fx.Status != 401 || fx.Body == "" {
		t.Fatalf("fixture is not a 401 with a body: %v", err)
	}
	return []byte(fx.Body)
}

// seedOpenAILane stores an openai-compatible key provider and its one lane
// in state available.
func seedOpenAILane(t *testing.T, reg *providerregistry.Registry, name string) {
	t.Helper()
	ctx := context.Background()
	rec := providerregistry.ProviderRecord{
		Name: name, Driver: providerregistry.DriverOpenAICompat, BaseURL: "https://example.invalid/v1",
		Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef("provider." + name + ".key"),
		AccountKind: providerregistry.AccountPersonal, Tier: providerregistry.TierMid,
		HealthStatus: providerregistry.HealthUnknown,
	}
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	lane := providerregistry.LaneRecord{LaneName: name, ProviderName: name, Weight: 1,
		Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable}
	if err := reg.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
}

func TestResolverOverMigratedRegistryRecordsLaneOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	dir := t.TempDir()
	clock := runtime.NewFixedClock(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	reg, closeReg := openLaneRegistry(t, dir, clock)
	seedOpenAILane(t, reg, "gw-openai")
	tr := &fixedTransport{status: 401, body: openai401(t)}

	err := callThroughResolver(t, reg, clock, tr, "gw-openai", "provider.gw-openai.key")
	closeReg()
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || tr.calls != 1 {
		t.Fatalf("call = %v after %d sends, want the real openai driver's KindPermissionDenied after one send", err, tr.calls)
	}
	reopened, closeAgain := openLaneRegistry(t, dir, clock)
	defer closeAgain()
	if state, reset := laneState(t, reopened, "gw-openai"); state != providerregistry.LaneStateAuthRequired || !reset.IsZero() {
		t.Fatalf("durable lane = %q reset %v after a 401 through the resolver, want auth-required with no reset", state, reset)
	}
}
