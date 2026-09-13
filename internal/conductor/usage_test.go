// Purpose: attributeUsage's own tests (P1-E11-W3-S23-T4): successful/
//
//	cancelled/errored one-shot dispatches, independent fail-open write
//	semantics, requesting-entity resolution, and the R-21.214 fan-out
//	invariant (n legs -> n rows, zero for the parent). Every assertion
//	about a write LANDING queries the PERSISTED ROW through a real
//	modernc-sqlite *UsageStore or *usage.Manager (Art.2/R-16.79) - never
//	an emitted bridge/audit event, which only proves a call happened.
//
// SPORT: conductor.usage-attribution/ADD (P1-E11-W3-S23-T4).
package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestUsage_SuccessfulDispatch asserts every UsageRecord field AND the
// matching IncrementUsage aggregate, both read back from their real
// persisted stores after one successful Execute call.
func TestUsage_SuccessfulDispatch(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	store := newUsageTestStore(t)
	mgr := newUsageAggregatorTestManager(t)
	exec.SetUsageStore(store).SetUsageAggregator(mgr).SetCostEstimator(fakeCostEstimator{cost: 42})

	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{
			Message: provider.ChatMessage{Role: "assistant", Content: "ok"},
			Usage:   provider.Usage{InputTokens: 10, OutputTokens: 20},
		}, nil
	}
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("resp.JobID is empty")
	}

	assertSuccessfulUsageRow(t, store, resp.JobID)
	assertSuccessfulUsageAggregate(t, mgr)
}

// assertSuccessfulUsageRow reads back the PERSISTED jobs_usage row for
// jobID and checks every UsageRecord field TestUsage_SuccessfulDispatch's
// dispatch should have produced.
func assertSuccessfulUsageRow(t *testing.T, store *UsageStore, jobID provider.JobID) {
	t.Helper()
	var rec UsageRecord
	var gotJobID string
	row := store.db.QueryRowContext(context.Background(),
		`SELECT job_id, lane_id, task_class, tokens_in, tokens_out, cost_micros, outcome_class, attempt, requesting_entity
			FROM `+tableUsage+` WHERE job_id = ?`, string(jobID))
	if err := row.Scan(&gotJobID, &rec.LaneID, &rec.TaskClass, &rec.TokensIn, &rec.TokensOut,
		&rec.CostMicroUSD, &rec.OutcomeClass, &rec.Attempt, &rec.RequestingEntity); err != nil {
		t.Fatalf("select persisted jobs_usage row: %v", err)
	}
	switch {
	case gotJobID != string(jobID):
		t.Errorf("job_id = %q, want %q", gotJobID, jobID)
	case rec.LaneID != "lane-1":
		t.Errorf("lane_id = %q, want lane-1", rec.LaneID)
	case rec.TaskClass != "chat":
		t.Errorf("task_class = %q, want chat", rec.TaskClass)
	case rec.TokensIn != 10 || rec.TokensOut != 20:
		t.Errorf("tokens = (%d, %d), want (10, 20)", rec.TokensIn, rec.TokensOut)
	case rec.CostMicroUSD != 42:
		t.Errorf("cost_micros = %d, want 42", rec.CostMicroUSD)
	case rec.OutcomeClass != outcomeUnknown:
		t.Errorf("outcome_class = %q, want %q", rec.OutcomeClass, outcomeUnknown)
	case rec.Attempt != 1:
		t.Errorf("attempt = %d, want 1", rec.Attempt)
	case rec.RequestingEntity != "standalone":
		t.Errorf("requesting_entity = %q, want standalone", rec.RequestingEntity)
	}
}

// assertSuccessfulUsageAggregate reads back the PERSISTED J/S-20.T4
// aggregate row via the real *usage.Manager's own QueryUsage.
func assertSuccessfulUsageAggregate(t *testing.T, mgr *usage.Manager) {
	t.Helper()
	summaries, err := mgr.QueryUsage(context.Background(), provider.UsageFilter{
		ProviderName: "test", LaneName: "lane-1", ModelName: "test-model",
	})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	s := summaries[0]
	if s.TokensIn != 10 || s.TokensOut != 20 || s.CostMicroUSD != 42 || s.Requests != 1 || s.Errors != 0 {
		t.Errorf("aggregate row = %+v, want {TokensIn:10 TokensOut:20 CostMicroUSD:42 Requests:1 Errors:0}", s)
	}
}

// TestUsage_CancelledDispatch simulates job.cancel firing mid-dispatch: the
// parent ctx is cancelled while the fake provider is blocked in Chat, and
// the fake returns whatever partial usage it received alongside ctx.Err().
// Execute's own dctx (derived from ctx) is cancelled the same way a real
// job.cancel call cancels it via e.cancels() (execute.go line ~93
// registers the SAME cancel func job.cancel invokes), so this exercises
// the real Execute code path, not a synthetic helper.
func TestUsage_CancelledDispatch(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	store := newUsageTestStore(t)
	exec.SetUsageStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	deps.prov.chatFn = func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 7, OutputTokens: 0}}, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = exec.Execute(ctx, validReq())
	}()
	<-started
	cancel()
	<-done

	var tokensIn, tokensOut int64
	var n int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+tableUsage).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("jobs_usage row count = %d, want 1", n)
	}
	if err := store.db.QueryRowContext(context.Background(), `SELECT tokens_in, tokens_out FROM `+tableUsage).
		Scan(&tokensIn, &tokensOut); err != nil {
		t.Fatalf("select persisted row: %v", err)
	}
	if tokensIn != 7 || tokensOut != 0 {
		t.Errorf("tokens = (%d, %d), want partial (7, 0)", tokensIn, tokensOut)
	}
}

// TestUsage_ProviderError asserts partial tokens from the error response
// AND that IncrementUsage is called with Error=true.
func TestUsage_ProviderError(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	store := newUsageTestStore(t)
	agg := &spyUsageAggregator{}
	exec.SetUsageStore(store).SetUsageAggregator(agg)

	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 3, OutputTokens: 0}}, errors.New("vendor exploded")
	}
	if _, err := exec.Execute(context.Background(), validReq()); err == nil {
		t.Fatal("Execute: want error, got nil")
	}

	var tokensIn, tokensOut int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT tokens_in, tokens_out FROM `+tableUsage).
		Scan(&tokensIn, &tokensOut); err != nil {
		t.Fatalf("select persisted row: %v", err)
	}
	if tokensIn != 3 || tokensOut != 0 {
		t.Errorf("tokens = (%d, %d), want partial (3, 0)", tokensIn, tokensOut)
	}
	calls := agg.snapshot()
	if len(calls) != 1 {
		t.Fatalf("IncrementUsage called %d times, want 1", len(calls))
	}
	if !calls[0].Error {
		t.Error("IncrementUsage called with Error=false, want true on a provider error")
	}
}

// TestUsage_WriteUsageRecordError_NonFatal: a failing UsageRecorder must
// not surface to Execute's caller, and must not block the independent
// aggregate write.
func TestUsage_WriteUsageRecordError_NonFatal(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	failingStore := &spyUsageStore{err: errors.New("disk full")}
	agg := &spyUsageAggregator{}
	exec.SetUsageStore(failingStore).SetUsageAggregator(agg)

	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute returned an error from a failing UsageRecorder: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("resp.JobID is empty")
	}
	if len(agg.snapshot()) != 1 {
		t.Fatalf("IncrementUsage called %d times, want 1 (must not be blocked by WriteUsageRecord failing)", len(agg.snapshot()))
	}
}

// TestUsage_IncrementUsageError_NonFatal: a failing UsageAggregator must
// not surface to Execute's caller, and must not block WriteUsageRecord's
// own (independently successful) outcome.
func TestUsage_IncrementUsageError_NonFatal(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	store := newUsageTestStore(t)
	failingAgg := &spyUsageAggregator{err: errors.New("aggregate store down")}
	exec.SetUsageStore(store).SetUsageAggregator(failingAgg)

	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute returned an error from a failing UsageAggregator: %v", err)
	}
	var n int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+tableUsage+` WHERE job_id = ?`,
		string(resp.JobID)).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("jobs_usage row count for job_id = %d, want 1 (WriteUsageRecord must not be blocked by IncrementUsage failing)", n)
	}
}

// TestUsage_EntityResolution asserts the priority order: plugin_id beats
// session_id beats "standalone".
func TestUsage_EntityResolution(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"neither set", context.Background(), "standalone"},
		{"session_id only", context.WithValue(context.Background(), sessionIDContextKey{}, "sess-1"), "session:sess-1"},
		{"plugin_id only", context.WithValue(context.Background(), pluginIDContextKey{}, "plug-1"), "plugin:plug-1"},
		{
			"both present, plugin_id wins",
			context.WithValue(context.WithValue(context.Background(), sessionIDContextKey{}, "sess-1"), pluginIDContextKey{}, "plug-1"),
			"plugin:plug-1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveRequestingEntity(c.ctx); got != c.want {
				t.Errorf("resolveRequestingEntity() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestUsage_FanOutParentWritesNoRow asserts R-21.214: for fan_out = n,
// exactly n jobs_usage rows exist (one per leg's own JobID, via Execute),
// and the fan-out parent's own JobID (assembleFanOutParent mints a fresh
// one, never reusing a leg's) owns none of them.
func TestUsage_FanOutParentWritesNoRow(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	cfg.Router = concurrencySafeRouter{}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	store := newUsageTestStore(t)
	exec.SetUsageStore(store)

	const n = 3
	parent, err := exec.ExecuteFanOutResponse(context.Background(), validReq(), n, nil, passthroughPermit, &spyJournal{})
	if err != nil {
		t.Fatalf("ExecuteFanOutResponse: %v", err)
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT job_id FROM `+tableUsage)
	if err != nil {
		t.Fatalf("query jobs_usage: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("jobs_usage row count = %d, want %d (one per leg, R-21.214)", len(seen), n)
	}
	if seen[string(parent.JobID)] {
		t.Errorf("jobs_usage has a row for the fan-out PARENT job_id %q; R-21.214 forbids this", parent.JobID)
	}
	for i, leg := range parent.Legs {
		if !seen[string(leg.JobID)] {
			t.Errorf("leg %d job_id %q has no jobs_usage row", i, leg.JobID)
		}
	}
}
