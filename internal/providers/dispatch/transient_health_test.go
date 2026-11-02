// Purpose: the transient-demotion acceptance tests (P1-GWY-09): the
//
//	consecutive-transient counter, the real router evicting a demoted
//	provider so the next call spills, and non-transient outcomes never
//	counting.
//
// Inputs: constructed transient errors, a scripted routing Transport and a
//
//	real registry, health.Manager, events bus and conductor router.
//
// Outputs: none.
// Constraints: no network; the clock is frozen; credentials are fakes.
//
// SPORT: internal/providers/dispatch transient_health/ADD (P1-GWY-09).

package dispatch

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/health"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingHealth records DemoteProvider calls; the rest of the interface is
// never reached by the counter.
type recordingHealth struct {
	provider.HealthManager
	mu    sync.Mutex
	calls []string
	err   error
}

func (h *recordingHealth) DemoteProvider(_ context.Context, name string, reason provider.DemotionReason) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, name+"/"+string(reason))
	return h.err
}

func (h *recordingHealth) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func transientErr() error {
	return provider.MarkTransient(cascade.New(cascade.KindUnavailable, "upstream 500"),
		provider.TransientInfo{Cause: provider.TransientHTTP5xx, Status: 500})
}

func observer(h provider.HealthManager, c *transientCounter, name string) *healthObserver {
	return &healthObserver{name: name, counter: c, health: h, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// assertConcurrentOutcomesDemoteEvery checks 50 concurrent transient outcomes demote once per DegradeAfter.
func assertConcurrentOutcomesDemoteEvery(ctx context.Context, t *testing.T) {
	t.Helper()
	h, c := &recordingHealth{}, &transientCounter{}
	o := observer(h, c, "P")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); o.observe(ctx, transientErr()) }()
	}
	wg.Wait()
	if h.count() != 50/DegradeAfter {
		t.Fatalf("demotions = %d, want %d", h.count(), 50/DegradeAfter)
	}
}

func TestTransientCounterDemotesAtThree(t *testing.T) {
	ctx := context.Background()
	t.Run("three in a row demotes exactly once", func(t *testing.T) {
		h, c := &recordingHealth{}, &transientCounter{}
		o := observer(h, c, "P")
		for i := 0; i < 3; i++ {
			o.observe(ctx, transientErr())
		}
		if len(h.calls) != 1 || h.calls[0] != "P/transient_upstream" {
			t.Fatalf("calls = %v, want exactly [P/transient_upstream]", h.calls)
		}
	})
	t.Run("two, a success, two demotes nothing", func(t *testing.T) {
		h, c := &recordingHealth{}, &transientCounter{}
		o := observer(h, c, "P")
		for _, err := range []error{transientErr(), transientErr(), nil, transientErr(), transientErr()} {
			o.observe(ctx, err)
		}
		if h.count() != 0 {
			t.Fatalf("calls = %v, want none", h.calls)
		}
	})
	t.Run("providers count independently", func(t *testing.T) {
		h, c := &recordingHealth{}, &transientCounter{}
		p, q := observer(h, c, "P"), observer(h, c, "Q")
		for i := 0; i < 2; i++ {
			p.observe(ctx, transientErr())
			q.observe(ctx, transientErr())
		}
		if h.count() != 0 {
			t.Fatalf("two each demoted %v", h.calls)
		}
		q.observe(ctx, transientErr())
		if len(h.calls) != 1 || h.calls[0] != "Q/transient_upstream" {
			t.Fatalf("calls = %v, want only Q", h.calls)
		}
	})
	t.Run("a demotion error never changes the count or panics", func(t *testing.T) {
		h, c := &recordingHealth{err: cascade.New(cascade.KindInternal, "boom")}, &transientCounter{}
		o := observer(h, c, "P")
		for i := 0; i < 6; i++ {
			o.observe(ctx, transientErr())
		}
		if h.count() != 2 {
			t.Fatalf("calls = %v, want 2 (the count resets after each attempt)", h.calls)
		}
	})
	t.Run("50 concurrent transient outcomes", func(t *testing.T) {
		assertConcurrentOutcomesDemoteEvery(ctx, t)
	})
}

func TestNonTransientNeverCounts(t *testing.T) {
	ctx := context.Background()
	for name, err := range map[string]error{
		"401":               cascade.New(cascade.KindPermissionDenied, "401"),
		"429":               cascade.New(cascade.KindQuotaExhausted, "429"),
		"capability denied": cascade.New(cascade.KindCapabilityDenied, "no tools"),
	} {
		t.Run(name, func(t *testing.T) {
			h, c := &recordingHealth{}, &transientCounter{}
			o := observer(h, c, "P")
			for i := 0; i < 10; i++ {
				o.observe(ctx, err)
			}
			if h.count() != 0 {
				t.Fatalf("%s demoted: %v", name, h.calls)
			}
			// It also resets: two transients before and after never add up.
			o.observe(ctx, transientErr())
			o.observe(ctx, transientErr())
			o.observe(ctx, err)
			o.observe(ctx, transientErr())
			o.observe(ctx, transientErr())
			if h.count() != 0 {
				t.Fatalf("%s did not reset the counter: %v", name, h.calls)
			}
		})
	}
}

// hostTransport answers by request host: P fails with 500, Q serves.
type hostTransport struct {
	mu   sync.Mutex
	hits map[string]int
}

func (h *hostTransport) Send(_ context.Context, _, url string, _ map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits == nil {
		h.hits = map[string]int{}
	}
	host := strings.TrimPrefix(url, "https://")[:1]
	h.hits[host]++
	if host == "p" {
		return 500, nil, io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"x"}}`)), nil
	}
	return 200, nil, io.NopCloser(bytes.NewReader([]byte(anthropicOK))), nil
}

// firstFree picks the first lane not excluded, in the order [lane-p lane-q].
type firstFree struct{}

func (firstFree) NextLane(_ context.Context, ex []conductor.LaneID) (conductor.LaneID, error) {
	for _, l := range []conductor.LaneID{"lane-p", "lane-q"} {
		skip := false
		for _, e := range ex {
			skip = skip || e == l
		}
		if !skip {
			return l, nil
		}
	}
	return "", conductor.ErrAllLanesExhausted
}

// newSpillRegistry builds a registry with providers and lanes p and q and their credentials.
func newSpillRegistry(ctx context.Context, t *testing.T, clk *testkit.FrozenClock) (*registry.Registry, *fakeCredentials) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatal(err)
	}
	reg := registry.NewRegistry(db, clk)
	creds := &fakeCredentials{values: map[string]string{}}
	for _, n := range []string{"p", "q"} {
		rec := registry.ProviderRecord{Name: n, Driver: registry.DriverAnthropic, BaseURL: "https://" + n + ".invalid",
			Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef(n + "-key"), KnownModels: []string{"model-a"},
			AccountKind: registry.AccountPersonal, Tier: registry.TierStrong, HealthStatus: registry.HealthUnknown}
		creds.values[rec.AuthRef.String()] = "cred-value"
		lane := registry.LaneRecord{LaneName: "lane-" + n, ProviderName: n, Weight: 1,
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}
		for _, err := range []error{reg.UpsertProvider(ctx, rec), reg.UpsertLane(ctx, lane)} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return reg, creds
}

// assertExactlyOneHealthEvent checks the subscription saw one provider-health event and no second.
func assertExactlyOneHealthEvent(t *testing.T, sub *events.Subscription) {
	t.Helper()
	select {
	case <-sub.Events:
	case <-time.After(5 * time.Second):
		t.Fatal("no provider-health changed event")
	}
	select {
	case ev := <-sub.Events:
		t.Fatalf("second health event %v, want exactly one", ev.Kind)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDegradedProviderEvictedAndSpilled(t *testing.T) {
	ctx := context.Background()
	clk := testkit.NewFrozenClock(laneHarnessNow)
	reg, creds := newSpillRegistry(ctx, t, clk)
	bus := events.New(storetest.NewMemStore(), clk)
	sub, err := bus.Subscribe(ctx, "provider-health", "", 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	mgr := health.NewManager(reg, bus, clk, 0)
	tr := &hostTransport{}
	res, err := NewResolver(reg, creds, clk, tr, WithHealth(mgr))
	if err != nil {
		t.Fatal(err)
	}
	res.log = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	router := conductor.NewRouter(registry.NewReader(reg), firstFree{}, nil, nil)
	req := provider.ModelRequest{TaskID: "t", TaskClass: "chat", Sensitivity: provider.SensitivityPublic,
		Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	call := func() (string, error) {
		sel, err := router.Select(ctx, req)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		p, err := res.Resolve(ctx, sel)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		_, cerr := p.Chat(ctx, provider.ChatRequest{Messages: req.Inputs})
		return string(sel.LaneID), cerr
	}
	for i := 0; i < DegradeAfter; i++ {
		lane, cerr := call()
		if _, ok := provider.TransientOf(cerr); lane != "lane-p" || !ok {
			t.Fatalf("call %d: lane %q err %v, want lane-p with a transient error", i, lane, cerr)
		}
	}
	if st, _ := mgr.GetHealth(ctx, "p"); st != provider.HealthDegraded {
		t.Fatalf("p health = %q, want degraded", st)
	}
	if lane, cerr := call(); lane != "lane-q" || cerr != nil {
		t.Fatalf("next call: lane %q err %v, want lane-q and success", lane, cerr)
	}
	assertExactlyOneHealthEvent(t, sub)
}
