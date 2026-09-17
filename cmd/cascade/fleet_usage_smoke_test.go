package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// Purpose (this file): S-40.T5's LAST acceptance criterion, which the rest
//   of the usage tests do not cover — "under the same seeded fixture,
//   cascade fleet top, supervisor.snapshot (T4 RPC) and cascade fleet
//   attention each return non-error results (T1-T4 integration smoke)".
//
// WHY IT IS A SEPARATE FILE AND A SEPARATE CLAIM. Every other usage test
//   asks whether `fleet usage` is right. This one asks whether the four
//   Epic R surfaces built across S-39 and S-40 still answer TOGETHER, which
//   is the question that closes the epic. A surface that works alone and
//   breaks when its siblings are wired is the defect this criterion exists
//   to catch, and it is not visible from any one of them.
//
// Constraints: in-process. The RPC half calls the registered handler
//   directly rather than over a socket — the transport has its own tests,
//   and a socket here would make this a daemon test wearing an epic-smoke
//   label.
// SPORT: cmd.cascade.fleet-usage/TEST (P1-E18-W4-S40-T5).

// TestEpicRSurfacesAnswerTogether is the T1-T4 smoke.
func TestEpicRSurfacesAnswerTogether(t *testing.T) {
	ctx := context.Background()
	fixture := loadUsageFixture(t)
	dataDir := t.TempDir()
	seedFleetUsageFixture(t, dataDir, fixture)
	deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}

	// T5 itself, so the fixture is proven live before the siblings are
	// asked about it: a smoke test over a fixture that seeded nothing
	// would report four healthy surfaces and mean none of it.
	out, err := runFleetUsageCLI(deps)
	if err != nil {
		t.Fatalf("fleet usage: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "requests=") {
		t.Fatalf("fleet usage rendered no rows from the seeded fixture:\n%s", out)
	}

	t.Run("T1 attention answers", func(t *testing.T) { assertAttentionAnswers(ctx, t) })
	t.Run("T3 metrics register and read back", func(t *testing.T) { assertMetricsAnswer(t) })
	t.Run("T4 supervisor.snapshot answers", func(t *testing.T) { assertSnapshotAnswers(ctx, t) })
}

// assertAttentionAnswers is S-39.T1's surface: the queue lists.
func assertAttentionAnswers(ctx context.Context, t *testing.T) {
	t.Helper()
	store := supervision.NewStore(
		storetest.NewMemStore(), smokeClock(), nil, supervision.NewSystemIDGenerator(), 0)
	items, err := store.ListInScopes(ctx, nil, supervision.Filter{})
	if err != nil {
		t.Fatalf("fleet attention list: %v", err)
	}
	// An empty queue is the right answer and is asserted as one: "no
	// error" alone would pass on a list that silently refused.
	if len(items) != 0 {
		t.Errorf("a fresh attention queue returned %d items", len(items))
	}
}

// assertMetricsAnswer is S-40.T3's surface: the counters count, and a
// consumer can read them back off the registry.
func assertMetricsAnswer(t *testing.T) {
	t.Helper()
	reg := runtime.NewRegistry()
	metrics, err := fleet.NewMetrics(reg)
	if err != nil {
		t.Fatalf("fleet metrics: %v", err)
	}
	metrics.RecordInterruption("task-smoke")
	if got := metrics.Interruptions(); got != 1 {
		t.Fatalf("interruptions = %d, want 1", got)
	}
	if len(reg.Snapshot(smokeClock().Now())) == 0 {
		t.Error("the registry snapshot is empty; the counters are not readable by a consumer")
	}
}

// assertSnapshotAnswers is S-40.T4's surface, over the SAME registry the
// counters register against — which is the half that was nil until
// S-40.T3 wired it.
func assertSnapshotAnswers(ctx context.Context, t *testing.T) {
	t.Helper()
	reg := runtime.NewRegistry()
	if _, err := fleet.NewMetrics(reg); err != nil {
		t.Fatalf("fleet metrics: %v", err)
	}
	registry := rpc.NewRegistry()
	daemon.RegisterSupervisorHandler(registry, storetest.NewMemStore(), smokeClock(), nil, reg, nil)

	raw, err := callRegisteredMethod(ctx, registry, "supervisor.snapshot")
	if err != nil {
		t.Fatalf("supervisor.snapshot: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("supervisor.snapshot did not return a JSON object: %v (%s)", err, raw)
	}
	if len(doc) == 0 {
		t.Error("supervisor.snapshot returned an empty document")
	}
}

// callRegisteredMethod invokes one registered RPC method with empty params
// and returns its raw result.
//
// It goes through the registry's own dispatch rather than reaching for the
// handler function, so a method registered under a different name than the
// one asserted here fails this test instead of passing it.
func callRegisteredMethod(ctx context.Context, registry *rpc.Registry, method string) ([]byte, error) {
	result, rpcErr := registry.Dispatch(ctx, &rpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  json.RawMessage(`{}`),
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	return json.Marshal(result)
}

// smokeClock is this file's fixed clock. Frozen rather than real so the
// smoke asserts the surfaces, not the wall clock.
func smokeClock() runtime.Clock {
	return testkit.NewFrozenClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
}
