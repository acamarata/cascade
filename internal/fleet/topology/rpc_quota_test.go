package topology

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

type fixedRPCClock struct{ t time.Time }

func (c fixedRPCClock) Now() time.Time { return c.t }

type storeDomainSource struct{ store *Store }

func (s storeDomainSource) ListQuotaDomains(ctx context.Context) ([]QuotaDomain, error) {
	return s.store.ListQuotaDomains(ctx)
}

// TestQuotaSnapshotRPCOverSocket is the named acceptance test (ARTICLE-2
// REAL COUNTERPART): fleet.quota.snapshot is exercised over a real
// *rpc.Registry.Dispatch call -- the SAME production dispatch path the
// daemon's POST /rpc unix-socket handler uses (rpc.NewRPCServer wraps
// this exact Registry) -- never a bare call to Handler. See this file's
// CONTRACT DEVIATION note and testdata/README.md for why this package's
// proof stops at Dispatch rather than a live net.Listen socket: identical
// scope decision to internal/fleet/capacity/rpc_test.go and
// internal/conversation/adapter_test.go before it. cmd/cascade's own
// composition-root integration test proves the daemon actually MOUNTS
// this method (see internal/daemon/quota_rpc_test.go and
// cmd/cascade/daemon_unix_quota_integration_test.go).
//
// CONTRACT DEVIATION (files_scope, recorded): the ticket's task 9 asks
// for "the real POST /rpc unix-socket server". internal/rpc's own
// sibling packages (capacity, sessions, journal) all stop at
// Registry.Dispatch for their in-package proof and add a separate
// integration-tagged file only when a live socket is the composition
// root's own concern (net is banned from this package's default unit
// lane) -- this file follows that established precedent; the daemon-level
// socket proof lives at the cmd/cascade composition root instead (see
// this ticket's journal).
func TestQuotaSnapshotRPCOverSocket(t *testing.T) {
	ctx := context.Background()
	topoStore := newTestStore(t)
	seedAccount(t, topoStore, "acct-1")
	domain := QuotaDomain{ID: "acct-1:api", AccountRef: "acct-1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}
	if err := topoStore.UpsertQuotaDomain(ctx, domain); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}

	quotaStore := NewQuotaStore(topoStore.rdb)
	seedFullDomain(t, quotaStore, domain.ID, map[string]float64{
		DimensionRPM: 0.9, DimensionTPM: 0.7, DimensionRPD: 0.5,
	})

	reg := rpc.NewRegistry()
	clock := fixedRPCClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	RegisterHandlers(reg, quotaStore, storeDomainSource{store: topoStore}, clock)

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetQuotaSnapshot, ID: json.RawMessage(`1`)}
	result, errObj := reg.Dispatch(ctx, req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodFleetQuotaSnapshot, errObj)
	}
	snap, ok := result.(QuotaSnapshot)
	if !ok {
		t.Fatalf("result type = %T, want QuotaSnapshot", result)
	}
	if len(snap.Domains) != 1 || snap.Domains[0].Confidence != 0.5 {
		t.Fatalf("snapshot = %+v, want one domain with confidence 0.5 (the minimum)", snap)
	}

	respBytes, err := json.Marshal(struct {
		Request json.RawMessage `json:"request"`
		Result  QuotaSnapshot   `json:"result"`
	}{Request: mustMarshalQuota(t, req), Result: snap})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	writeQuotaFixture(t, respBytes)
}

func mustMarshalQuota(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// writeQuotaFixture writes b to testdata/fixture_quota_snapshot_rpc.json.
// The clock and seeded rows above are fixed, so the content is
// deterministic across runs -- see testdata/README.md for provenance.
func writeQuotaFixture(t *testing.T, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "fixture_quota_snapshot_rpc.json")
	var pretty map[string]any
	if err := json.Unmarshal(b, &pretty); err != nil {
		t.Fatalf("unmarshal for pretty-print: %v", err)
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		t.Fatalf("marshal indent: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestQuotaSnapshotRPCDaemonNotReady(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, nil, nil, nil)
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: MethodFleetQuotaSnapshot})
	if errObj == nil {
		t.Fatal("expected a typed error for a nil store, got none")
	}
}

func TestQuotaSnapshotRPCMalformedRequestIsMethodNotFound(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, nil, nil, nil)
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: "fleet.quota.bogus"})
	if errObj == nil {
		t.Fatal("a malformed/unknown JSON-RPC 2.0 request must return the A-T7 error code, never a panic")
	}
}

func TestQuotaSnapshotRPCPropagatesListError(t *testing.T) {
	failing := failingDomainSource{err: errors.New("list failed")}
	reg := rpc.NewRegistry()
	quotaStore := newQuotaTestStore(t)
	RegisterHandlers(reg, quotaStore, failing, fixedRPCClock{t: time.Now()})
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: MethodFleetQuotaSnapshot})
	if errObj == nil {
		t.Fatal("expected the ListQuotaDomains error to propagate")
	}
}

type failingDomainSource struct{ err error }

func (f failingDomainSource) ListQuotaDomains(context.Context) ([]QuotaDomain, error) {
	return nil, f.err
}
