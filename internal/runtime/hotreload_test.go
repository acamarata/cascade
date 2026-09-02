package runtime

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// --- HotReloader: end-to-end accept/reject scenarios ---

type fakeEventPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	name    string
	payload map[string]interface{}
}

func (f *fakeEventPublisher) Publish(_ context.Context, name string, payload map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, publishedEvent{name: name, payload: payload})
}

func (f *fakeEventPublisher) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.name
	}
	return out
}

func newTestHotReloader(t *testing.T, initialTOML string) (*HotReloader, string, *fakeEventPublisher, *StoreAuditRecorder, *storetest.MemStore) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = writeFile(t, path, initialTOML)

	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)}
	initial, err := Load(context.Background(), opts)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	store := storetest.NewMemStore()
	clock := NewFixedClock(time.Unix(0, 0))
	events := &fakeEventPublisher{}
	audit := NewStoreAuditRecorder(store, clock)

	hr := NewHotReloader(path, opts, initial, clock, events, audit, nil)
	return hr, path, events, audit, store
}

func TestHotReloader_TighteningAccepted(t *testing.T) {
	// conductor.external_routing_enabled has a real ratified order
	// (false=tight, true=loose); start loose and tighten it, which must
	// accept — unlike the any-change families (policy/secrets/nodes),
	// which flag every change including a tightening one (see
	// TestCompareSecurity_Policy).
	hr, path, events, _, store := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = true\n")
	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")

	outcome := hr.Reload(context.Background())
	if !outcome.Accepted || outcome.Rejected {
		t.Fatalf("expected accepted, got %+v", outcome)
	}
	found := false
	for _, n := range events.names() {
		if n == eventReloadAccepted {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected config.reload.accepted event, got %v", events.names())
	}
	// Audit record persisted.
	it, err := store.Scan(context.Background(), auditNamespace, auditKindReloadAccept+"/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	if !it.Next(context.Background()) {
		t.Fatal("expected an audit record for the accepted reload")
	}
}

func TestHotReloader_LooseningRejected(t *testing.T) {
	hr, path, events, _, store := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	before := hr.Current()

	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = true\n")
	outcome := hr.Reload(context.Background())
	if outcome.Accepted || !outcome.Rejected {
		t.Fatalf("expected rejected, got %+v", outcome)
	}
	if len(outcome.LooseningPaths) == 0 {
		t.Fatal("expected non-empty LooseningPaths")
	}
	if hr.Current() != before {
		t.Fatal("running config must be unchanged on a rejected reload")
	}
	found := false
	for _, n := range events.names() {
		if n == eventReloadRejected {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected config.reload.rejected event, got %v", events.names())
	}
	it, err := store.Scan(context.Background(), auditNamespace, auditKindReloadReject+"/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	if !it.Next(context.Background()) {
		t.Fatal("expected an audit record for the rejected reload")
	}
}

func TestHotReloader_InvalidTOMLRejected(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[logging]\nlevel = \"info\"\n")
	before := hr.Current()
	_ = writeFile(t, path, "not valid toml {{{")

	outcome := hr.Reload(context.Background())
	if !outcome.Rejected {
		t.Fatalf("expected rejected, got %+v", outcome)
	}
	if hr.Current() != before {
		t.Fatal("running config must be unchanged on invalid TOML")
	}
}

func TestHotReloader_ElevationChangeAlwaysRejected_EvenTightening(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[elevation]\nallow_remote = true\nhelper_pubkey = \"k1\"\n")
	before := hr.Current()

	// This is a TIGHTENING change (true->false) yet must still be
	// rejected: elevation requires an attestation, never a plain
	// hot-reload, regardless of direction.
	_ = writeFile(t, path, "[elevation]\nallow_remote = false\n")
	outcome := hr.Reload(context.Background())
	if !outcome.Rejected {
		t.Fatalf("expected rejected (elevation change, any direction), got %+v", outcome)
	}
	if hr.Current() != before {
		t.Fatal("running config must be unchanged")
	}
}

func TestHotReloader_ColdKeyChange_RestartRequired(t *testing.T) {
	hr, path, events, _, _ := newTestHotReloader(t, "[storage]\ndriver = \"sqlite\"\n")
	_ = writeFile(t, path, "[storage]\ndriver = \"postgres\"\n")

	outcome := hr.Reload(context.Background())
	if !outcome.Accepted {
		t.Fatalf("cold-only change with no loosening should still accept (hot keys applied, cold frozen), got %+v", outcome)
	}
	if len(outcome.RestartKeys) == 0 {
		t.Fatal("expected restart-required keys naming the changed cold section")
	}
	// The cold section must NOT have taken effect in the swapped snapshot.
	if sectionAt(hr.Current().Extra, "storage")["driver"] != "sqlite" {
		t.Fatalf("cold section must stay frozen at the old value, got %v", hr.Current().Extra["storage"])
	}
	foundRestart := false
	for _, n := range events.names() {
		if n == eventRestartRequired {
			foundRestart = true
		}
	}
	if !foundRestart {
		t.Fatalf("expected config.restart.required event, got %v", events.names())
	}
}

func TestHotReloader_ColdKeyDiff_RuntimeProfile(t *testing.T) {
	old := &Config{Runtime: runtimeSection{Profile: ProfileLocal}}
	proposed := &Config{Runtime: runtimeSection{Profile: ProfileServer}}
	keys := coldKeyDiff(old, proposed)
	if len(keys) != 1 || keys[0] != "runtime.profile" {
		t.Fatalf("got %v", keys)
	}
}

func TestHotReloader_LoggingHotApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = writeFile(t, path, "[logging]\nlevel = \"info\"\n")
	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)}
	initial, err := Load(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := NewLogProvider(initial.Logging, newTestPathProvider(t), NewFixedClock(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()

	hr := NewHotReloader(path, opts, initial, NewFixedClock(time.Unix(0, 0)), nil, nil, logs)
	_ = writeFile(t, path, "[logging]\nlevel = \"debug\"\n")
	outcome := hr.Reload(context.Background())
	if !outcome.Accepted {
		t.Fatalf("expected accepted, got %+v", outcome)
	}
	found := false
	for _, s := range outcome.AppliedLive {
		if s == "logging" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected logging to be reported as genuinely applied live, got %v", outcome.AppliedLive)
	}
}

// --- Concurrency: Current() during a concurrent Reload, under -race ---

func TestHotReloader_ConcurrentReadDuringReload(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = hr.Current() // must never observe a torn/partial value
			}
		}
	}()

	for i := 0; i < 20; i++ {
		_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")
		hr.Reload(context.Background())
	}
	close(stop)
	wg.Wait()
}

// newTestPathProvider builds a PathProvider rooted at t.TempDir() (Art.7.1).
func newTestPathProvider(t *testing.T) PathProvider {
	t.Helper()
	dir := t.TempDir()
	pp, err := NewPathProvider(func(string) string { return "" }, func() (string, error) { return dir, nil })
	if err != nil {
		t.Fatal(err)
	}
	return pp
}
