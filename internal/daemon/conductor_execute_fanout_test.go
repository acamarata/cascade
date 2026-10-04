//go:build !windows && integration

package daemon

// Purpose: P1-CORE-19 crash-site and claim acceptance for the real
//   conductor.execute fan-out handler. A child test binary runs the handler
//   over a SQLite store that SIGKILLs its own process when a named write
//   commits; the parent reopens the store and runs resume.Scan and Sweep.
//   The live-call claim test runs the handler and Sweep over one store.
//   The provider is in-process (recorded-provider HTTP legs: cmd/cascade).
// SPORT: internal/daemon (CHANGE, P1-CORE-19).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

const crashID, crashTTL = "01ARZ3NDEKTSV4RRFFQ69G5FAV", time.Hour

// laneReader is a registry with one healthy lane, "mem".
type laneReader struct{}

func (laneReader) GetProvider(context.Context, string) (provider.ProviderInfo, error) {
	return provider.ProviderInfo{Name: "mem", HealthStatus: "healthy"}, nil
}
func (laneReader) ListProviders(context.Context) ([]provider.ProviderInfo, error) {
	return []provider.ProviderInfo{{Name: "mem", HealthStatus: "healthy"}}, nil
}
func (laneReader) ListLanes(context.Context) ([]provider.LaneInfo, error) {
	return []provider.LaneInfo{{LaneName: "mem", ProviderName: "mem"}}, nil
}
func (laneReader) ListPool(context.Context, string) ([]provider.LaneInfo, error) { return nil, nil }
func (laneReader) GetByModel(context.Context, string) ([]provider.ProviderInfo, error) {
	return nil, nil
}

type laneQuota struct{}

func (laneQuota) NextLane(context.Context, []conductor.LaneID) (conductor.LaneID, error) {
	return "mem", nil
}

// memProvider answers Chat with one reply, holding each call on hold
// (when set) until it is closed or the call's ctx ends.
type memProvider struct {
	provider.ModelProvider
	calls   atomic.Int32
	hold    chan struct{}
	entered chan struct{}
}

func (p *memProvider) Chat(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls.Add(1)
	p.entered <- struct{}{}
	if p.hold != nil {
		select {
		case <-p.hold:
		case <-ctx.Done():
			return provider.ChatResponse{}, ctx.Err()
		}
	}
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}, nil
}

type memResolver struct{ p *memProvider }

func (r memResolver) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	return r.p, nil
}

// newMemHandler registers conductor.execute over store with p.
func newMemHandler(t *testing.T, store provider.Store, p *memProvider) (*rpc.Registry, ConductorFanOut) {
	t.Helper()
	reg, clock := rpc.NewRegistry(), runtime.NewSystemClock()
	fan := NewConductorFanOut(store, clock)
	if err := RegisterConductorExecuteHandler(reg, NewManifest(nil, clock), laneReader{}, laneQuota{}, memResolver{p},
		newTestAuditWriter(t), clock, fullSecurity(t), ConductorAccounting{}, fan); err != nil {
		t.Fatal(err)
	}
	return reg, fan
}

func memCall(ctx context.Context, reg *rpc.Registry, n int) (any, *rpc.ErrorObject) {
	raw, _ := json.Marshal(map[string]any{"task_id": "t", "task_class": "chat", "sensitivity": "public", "fan_out": n,
		"request_id": crashID, "inputs": []map[string]string{{"role": "user", "content": "crash prompt"}}})
	return reg.Dispatch(ctx, &rpc.Request{Method: ConductorExecuteMethod, Params: raw})
}

// killStore SIGKILLs this process once a committed write matches site.
type killStore struct {
	provider.Store
	site  string
	dones int
}

type recTx struct {
	provider.Tx
	vals *[]string
}

func (r recTx) Put(ctx context.Context, ns, key string, v []byte) error {
	*r.vals = append(*r.vals, ns+"|"+string(v))
	return r.Tx.Put(ctx, ns, key, v)
}

func (r recTx) CompareAndSwap(ctx context.Context, ns, key string, old, v []byte) error {
	*r.vals = append(*r.vals, ns+"|"+string(v))
	return r.Tx.CompareAndSwap(ctx, ns, key, old, v)
}

func (k *killStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	var vals []string
	err := k.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, recTx{Tx: tx, vals: &vals}) })
	for _, v := range vals {
		if err == nil && k.matches(v) {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	return err
}

func (k *killStore) matches(v string) bool {
	if k.site == "lastdone" && strings.Contains(v, "result_key") && !strings.HasPrefix(v, "conductor.fanout.") {
		k.dones++
	}
	return k.site == "cursor" && strings.Contains(v, "request_key") && !strings.HasPrefix(v, "conductor.fanout.") ||
		k.site == "request" && strings.HasPrefix(v, "conductor.fanout.requests|") ||
		k.site == "lastdone" && k.dones == 2 || k.site == "marker" && strings.Contains(v, "fanout_final")
}

// TestFanOutCrashChild is the child half (parent env only; killed by its store).
func TestFanOutCrashChild(t *testing.T) {
	site, path := os.Getenv("CASCADE_TEST_CRASH_SITE"), os.Getenv("CASCADE_TEST_CRASH_DB")
	if site == "" {
		t.Skip("child half of the fan-out crash tests")
	}
	drv, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := newMemHandler(t, &killStore{Store: drv, site: site}, &memProvider{entered: make(chan struct{}, 16)})
	_, errObj := memCall(context.Background(), reg, 2)
	t.Fatalf("child survived site %s: %+v", site, errObj)
}

// crashAt runs the child to site and returns the reopened store's deps.
func crashAt(t *testing.T, site string) (resume.FanOutDeps, *resume.FanOutStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFanOutCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CASCADE_TEST_CRASH_SITE="+site, "CASCADE_TEST_CRASH_DB="+path)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	var exitErr *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exitErr) || !exitErr.Sys().(syscall.WaitStatus).Signaled() {
		t.Fatalf("child at %s: %v, want SIGKILL\n%s", site, err, out.String())
	}
	drv, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	clock := runtime.NewSystemClock()
	js := journal.New(drv, clock, journal.DefaultNamespace)
	fs, _ := resume.NewFanOutStore(js, drv)
	return resume.FanOutDeps{Store: drv, Heads: js, Claims: resume.NewClaims(), Clock: clock}, fs
}

// crashVerdict scans, sweeps past the ttl, and returns class/kept/state.
func crashVerdict(t *testing.T, site string) (resume.FanOutClass, int, resume.FanOutState) {
	t.Helper()
	deps, fs := crashAt(t, site)
	ctx := context.Background()
	got, err := resume.Scan(ctx, deps)
	if err != nil || len(got) != 1 {
		t.Fatalf("%s: Scan = (%+v, %v), want one verdict", site, got, err)
	}
	kept := countNS(t, deps.Store, "conductor.fanout.legs") + countNS(t, deps.Store, "conductor.fanout.requests")
	if _, err := resume.Sweep(ctx, deps, time.Now().Add(crashTTL+time.Minute), crashTTL); err != nil {
		t.Fatalf("%s: Sweep: %v", site, err)
	}
	if n := countNS(t, deps.Store, "conductor.fanout.legs") + countNS(t, deps.Store, "conductor.fanout.requests"); n != 0 {
		t.Fatalf("%s: %d orphan records after Sweep", site, n)
	}
	st, _ := fs.State(ctx, crashID)
	return got[0].Class, kept, st
}

func countNS(t *testing.T, s provider.Store, ns string) (n int) {
	t.Helper()
	it, err := s.Scan(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	for it.Next(context.Background()) {
		n++
	}
	return n
}

func TestFanOutProducerCrashAfterCursor(t *testing.T) {
	if class, kept, st := crashVerdict(t, "cursor"); class != resume.FanOutTerminal || kept != 0 || st.Final != resume.OutcomeTerminal {
		t.Fatalf("class %d kept %d marker %q; want Terminal-and-cleaned", class, kept, st.Final)
	}
}

func TestFanOutProducerCrashAfterRequestRecord(t *testing.T) {
	if class, kept, st := crashVerdict(t, "request"); class != resume.FanOutResumable || kept != 1 || st.Final != resume.OutcomeExpired {
		t.Fatalf("class %d kept %d marker %q; want Resumable, then expired by the sweep", class, kept, st.Final)
	}
}

func TestFanOutProducerCrashAfterLastDone(t *testing.T) {
	deps, _ := crashAt(t, "lastdone")
	if _, err := resume.Sweep(context.Background(), deps, time.Now(), crashTTL); err != nil || countNS(t, deps.Store, "conductor.fanout.legs") != 2 {
		t.Fatalf("complete-undelivered not retained before the ttl: %v", err)
	}
	if class, kept, st := crashVerdict(t, "lastdone"); class != resume.FanOutCompleteUndelivered || kept != 3 || st.Final != resume.OutcomeExpired {
		t.Fatalf("class %d kept %d marker %q; want complete-undelivered retained, then expired", class, kept, st.Final)
	}
}

func TestFanOutProducerCrashBeforeDelete(t *testing.T) {
	if class, kept, st := crashVerdict(t, "marker"); class != resume.FanOutFinalMarked || kept != 0 || st.Final != resume.OutcomeDelivered {
		t.Fatalf("class %d kept %d marker %q; want final-marked and deleted", class, kept, st.Final)
	}
}

// gateStore blocks the first read naming the fan-out until gate closes.
type gateStore struct {
	provider.Store
	armed, held chan struct{}
}

func (g gateStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if strings.Contains(prefix, crashID) {
		select {
		case <-g.armed:
			g.held <- struct{}{}
			<-g.held
		default:
		}
	}
	return g.Store.Scan(ctx, ns, prefix)
}

func TestConductorExecuteFanOutSweepVsLiveHandler(t *testing.T) {
	drv, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := &memProvider{hold: make(chan struct{}), entered: make(chan struct{}, 16)}
	reg, fan := newMemHandler(t, drv, p)
	live := make(chan *rpc.ErrorObject, 1)
	go func() { _, e := memCall(context.Background(), reg, 2); live <- e }()
	<-p.entered
	<-p.entered // both legs started: the live call writes nothing more until release
	head, _ := fan.Journal.HeadSeq(context.Background(), resume.FanOutEntity(crashID))
	deps := resume.FanOutDeps{Store: drv, Heads: fan.Journal, Claims: fan.Claims, Clock: runtime.NewSystemClock()}
	if _, err := resume.Sweep(context.Background(), deps, time.Now().Add(crashTTL*2), crashTTL); err != nil {
		t.Fatal(err)
	}
	if h, _ := fan.Journal.HeadSeq(context.Background(), resume.FanOutEntity(crashID)); h != head || countNS(t, drv, "conductor.fanout.requests") != 1 {
		t.Fatalf("Sweep touched the live fan-out: head %d -> %d", head, h)
	}
	close(p.hold)
	if e := <-live; e != nil {
		t.Fatalf("live call: %+v, want delivery", e)
	}
	g := gateStore{Store: drv, armed: make(chan struct{}), held: make(chan struct{})}
	close(g.armed)
	deps.Store = g
	go func() { _, _ = resume.Sweep(context.Background(), deps, time.Now(), crashTTL) }()
	<-g.held // Sweep now holds the claim for crashID
	calls := p.calls.Load()
	if _, e := memCall(context.Background(), reg, 2); e == nil || !strings.Contains(e.Message, "fan-out in progress") || p.calls.Load() != calls {
		t.Fatalf("re-attach while Sweep holds the id: %+v; want conflict and zero calls", e)
	}
	g.held <- struct{}{}
}
