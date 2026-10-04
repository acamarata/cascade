package audit

// Purpose: fault injection for the effect recorder: a process killed
//   between effect and confirm (the recipe every consumer copies), a
//   transaction failing part-way, store failures, and sixteen racing writers.
// Inputs: CASCADE_EFFECT_CHILD=<store path> makes this binary the child
//   (providers/agents/conformance re-exec precedent); no production hook.
// Constraints: stored state is asserted, never events; no sleeps.
// SPORT: internal.audit.EffectLog/ADDED (tests) (P1-SEC-33).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

const (
	effectChildEnv = "CASCADE_EFFECT_CHILD"
	crashKey       = "clipboard.clear:crash-1"
	childCrashCode = 3
)

// runEffectChild begins, performs the recorded effect, then dies before
// ConfirmEffect. Exit codes other than 3 name the step that failed.
func runEffectChild(dbPath string) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		os.Exit(4)
	}
	el, err := NewEffectLog(New(store, testkit.NewFrozenClock(testInstant), nil))
	if err != nil {
		os.Exit(5)
	}
	if _, err := el.BeginEffect(ctx, effectReq(crashKey)); err != nil {
		os.Exit(6)
	}
	if err := performRecordedEffect(filepath.Dir(dbPath)); err != nil {
		os.Exit(7)
	}
	os.Exit(childCrashCode)
}

// performRecordedEffect is the recording fake: one line per run.
func performRecordedEffect(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, "effects.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("cleared\n")
	return errors.Join(err, f.Close())
}

func TestEffectCrashBetweenEffectAndConfirm(t *testing.T) {
	if path := os.Getenv(effectChildEnv); path != "" {
		runEffectChild(path)
		return
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEffectCrashBetweenEffectAndConfirm$", "-test.count=1")
	cmd.Env = append(os.Environ(), effectChildEnv+"="+dbPath)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != childCrashCode {
		t.Fatalf("child = %v, want exit status %d; output:\n%s", err, childCrashCode, out)
	}

	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening the crashed store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	el, log := newEffectLog(t, store)
	if phase, found, err := el.EffectState(ctx, crashKey); err != nil || !found || phase != EffectIntent {
		t.Fatalf("EffectState after crash = %q, %v, %v; want intent", phase, found, err)
	}
	pending, err := el.PendingEffects(ctx)
	if err != nil || len(pending) != 1 || pending[0].Key != crashKey {
		t.Fatalf("PendingEffects = %+v, %v; want exactly %q", pending, err, crashKey)
	}
	// Recovery: this fake has no site probe, so the intent is unknown-outcome.
	if _, err := el.MarkUnknownOutcome(ctx, pending[0]); err != nil {
		t.Fatalf("MarkUnknownOutcome: %v", err)
	}
	// A retry is refused, so the effect cannot run a second time.
	if _, err := el.BeginEffect(ctx, effectReq(crashKey)); err == nil {
		_ = performRecordedEffect(dir)
		t.Fatal("BeginEffect after unknown-outcome succeeded")
	} else {
		requireSentinel(t, err, ErrEffectExists)
	}
	assertCrashRecovered(t, el, log, store, dir)
}

func assertCrashRecovered(t *testing.T, el *EffectLog, log *Log, store provider.Store, dir string) {
	t.Helper()
	ctx := context.Background()
	got := storedEffects(t, store, crashKey)
	if got[EffectIntent] != 1 || got[EffectUnknown] != 1 || len(got) != 2 {
		t.Fatalf("stored effect records = %v, want one intent and one unknown-outcome", got)
	}
	if phase, _, err := el.EffectState(ctx, crashKey); err != nil || phase != EffectUnknown {
		t.Fatalf("EffectState after recovery = %q, %v; want unknown-outcome", phase, err)
	}
	if pending, err := el.PendingEffects(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("PendingEffects after recovery = %+v, %v; want none", pending, err)
	}
	ran, err := os.ReadFile(filepath.Join(dir, "effects.log"))
	if err != nil || strings.Count(string(ran), "cleared\n") != 1 {
		t.Fatalf("the effect ran %q (%v), want exactly once", ran, err)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify after recovery: %v", err)
	}
}

var errInjected = cascade.New(cascade.KindUnavailable, "injected transaction failure")

// failAfterWrite performs each write whose key has prefix, then fails the
// transaction, so a write already made inside it must be rolled back.
type failAfterWrite struct {
	provider.Store
	prefix string
}

func (f *failAfterWrite) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return f.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, &failTx{Tx: tx, prefix: f.prefix})
	})
}

type failTx struct {
	provider.Tx
	prefix string
}

func (f *failTx) after(key string, err error) error {
	if err == nil && strings.HasPrefix(key, f.prefix) {
		return errInjected
	}
	return err
}

func (f *failTx) Put(ctx context.Context, ns, key string, v []byte) error {
	return f.after(key, f.Tx.Put(ctx, ns, key, v))
}

func (f *failTx) CompareAndSwap(ctx context.Context, ns, key string, old, v []byte) error {
	return f.after(key, f.Tx.CompareAndSwap(ctx, ns, key, old, v))
}

func TestEffectIndexCommitsWithRecord(t *testing.T) {
	for _, s := range effectStores {
		for _, prefix := range []string{effectPrefix, recordPrefix, indexPrefix, headKey} {
			t.Run(s.name+"/"+prefix, func(t *testing.T) {
				ctx := context.Background()
				store := s.open(t)
				bad, _ := newEffectLog(t, &failAfterWrite{Store: store, prefix: prefix})
				if _, err := bad.BeginEffect(ctx, effectReq("plugin.install:p-1")); err == nil {
					t.Fatal("BeginEffect through a failing transaction succeeded")
				}
				if left := scanNamespace(t, store, ""); len(left) != 0 {
					t.Fatalf("a failed intent left %d stored keys: %v", len(left), left)
				}
				good, log := newEffectLog(t, store)
				h, err := good.BeginEffect(ctx, effectReq("plugin.install:p-1"))
				if err != nil {
					t.Fatalf("BeginEffect after the failed one: %v", err)
				}
				bad2, _ := newEffectLog(t, &failAfterWrite{Store: store, prefix: prefix})
				if _, err := bad2.ConfirmEffect(ctx, h, "installed"); err == nil {
					t.Fatal("ConfirmEffect through a failing transaction succeeded")
				}
				if got := storedEffects(t, store, h.Key); got[EffectIntent] != 1 || len(got) != 1 {
					t.Fatalf("a failed confirm left records %v, want the intent alone", got)
				}
				if phase, _, err := good.EffectState(ctx, h.Key); err != nil || phase != EffectIntent {
					t.Fatalf("EffectState after a failed confirm = %q, %v; want intent", phase, err)
				}
				if err := log.Verify(ctx); err != nil {
					t.Fatalf("Verify: %v", err)
				}
			})
		}
	}
}

func TestBeginEffectConcurrentOneWins(t *testing.T) {
	for _, s := range effectStores {
		t.Run(s.name+"/shared-log", func(t *testing.T) { raceBegin(t, s.open(t), true) })
		t.Run(s.name+"/log-per-writer", func(t *testing.T) { raceBegin(t, s.open(t), false) })
	}
}

// raceBegin releases sixteen BeginEffect calls on one key from one barrier;
// shared=false gives each writer its own *Log, as separate processes have.
func raceBegin(t *testing.T, store provider.Store, shared bool) {
	const writers, key = 16, "agent.spawn:race-1"
	ctx := context.Background()
	logs := make([]*EffectLog, writers)
	for i := range logs {
		if i == 0 || !shared {
			logs[i], _ = newEffectLog(t, store)
		} else {
			logs[i] = logs[0]
		}
	}
	results := make([]struct {
		h   EffectHandle
		err error
	}, writers)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for i := range writers {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results[i].h, results[i].err = logs[i].BeginEffect(ctx, effectReq(key))
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	var won []EffectHandle
	for _, r := range results {
		if r.err == nil {
			won = append(won, r.h)
			continue
		}
		requireSentinel(t, r.err, ErrEffectExists)
	}
	if len(won) != 1 {
		t.Fatalf("%d of %d BeginEffect calls won, want exactly 1", len(won), writers)
	}
	assertOneIntent(t, store, key, won[0])
}

func assertOneIntent(t *testing.T, store provider.Store, key string, won EffectHandle) {
	t.Helper()
	if got := storedEffects(t, store, key); got[EffectIntent] != 1 || len(got) != 1 {
		t.Fatalf("stored effect records = %v, want exactly one intent", got)
	}
	rows := scanNamespace(t, store, effectPrefix)
	row, err := decodeRow(rows[effectKey(key)])
	if len(rows) != 1 || err != nil || row.Phase != EffectIntent || row.IntentSeq != won.IntentSeq {
		t.Fatalf("index rows = %q (%v), want one intent row at seq %d", rows, err, won.IntentSeq)
	}
	if err := New(store, testkit.NewFrozenClock(testInstant), nil).Verify(context.Background()); err != nil {
		t.Fatalf("Verify after the race: %v", err)
	}
}

func TestEffectStoreFailuresRefuse(t *testing.T) {
	ctx := context.Background()
	fs := &failingStore{Store: storetest.NewMemStore()}
	el, _ := newEffectLog(t, fs)
	h, err := el.BeginEffect(ctx, effectReq("node.exec:n-2"))
	if err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	fs.failGet = true
	_, _, stateErr := el.EffectState(ctx, h.Key)
	_, confirmErr := el.ConfirmEffect(ctx, h, "ran")
	fs.failGet, fs.failScan = false, true
	_, pendingErr := el.PendingEffects(ctx)
	fresh, _ := newEffectLog(t, &failingStore{Store: storetest.NewMemStore(), failGet: true})
	_, beginErr := fresh.BeginEffect(ctx, effectReq("node.exec:n-3"))
	for _, err := range []error{stateErr, confirmErr, pendingErr, beginErr} {
		requireSentinel(t, err, ErrStoreUnavailable)
	}
	redacting, _ := NewEffectLog(NewWithRedactor(storetest.NewMemStore(), testkit.NewFrozenClock(testInstant),
		nil, nilReturningRedactor{}))
	if _, err := redacting.BeginEffect(ctx, effectReq("node.exec:n-4")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("BeginEffect with a failing redactor = %v, want KindIntegrity", err)
	}
	noisy, _ := NewEffectLog(New(storetest.NewMemStore(), testkit.NewFrozenClock(testInstant), failingBus{}))
	if bh, err := noisy.BeginEffect(ctx, effectReq("node.exec:n-5")); err == nil || bh.IntentSeq != 1 {
		t.Fatalf("a committed intent whose notification failed = %+v, %v; want the handle and an error", bh, err)
	}
}
