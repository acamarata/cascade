package audit

// Purpose: two operating-system processes appending to one audit store.
//   The sqlite driver takes an exclusive lock per open store, so the
//   processes take turns: each child opens the store (retrying while the
//   other holds it), appends ten records and closes it, five times over.
//   Every open numbers its records from the stored head, so the chain the
//   parent verifies afterwards holds all hundred records with no fork.
// Inputs: CASCADE_AUDIT_CHILD=<store path> makes TestAuditAppendChild the
//   child entry (the re-exec precedent of effect_crash_test.go); it skips
//   when the variable is unset.
// Constraints: the only waiting is the bounded backoff on a lock held by
//   the other child; stored state is asserted, never events.
// SPORT: internal.audit.Log/ADDED (tests) (P1-SEC-06).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

const (
	auditChildEnv   = "CASCADE_AUDIT_CHILD"
	childRounds     = 5
	childAppends    = 10
	childOpenBudget = 60 * time.Second
)

// openWithBackoff opens the store, retrying a lock the other child holds
// with a bounded, growing pause. Any other failure, or the budget running
// out, is returned.
func openWithBackoff(ctx context.Context, path string) (*sqlite.Driver, error) {
	deadline := time.Now().Add(childOpenBudget)
	for pause := time.Millisecond; ; pause = min(pause*2, 50*time.Millisecond) {
		store, err := sqlite.Open(ctx, path)
		if err == nil {
			return store, nil
		}
		if !cascade.HasKind(err, cascade.KindConflict) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(pause)
	}
}

// TestAuditAppendChild is the child entry; it only runs when the parent
// test re-executes this binary with CASCADE_AUDIT_CHILD set.
func TestAuditAppendChild(t *testing.T) {
	path := os.Getenv(auditChildEnv)
	if path == "" {
		t.Skip(auditChildEnv + " is not set: this entry runs only as the child of TestAuditTwoProcessesAppendOverOneStore")
	}
	ctx := context.Background()
	for round := 0; round < childRounds; round++ {
		store, err := openWithBackoff(ctx, path)
		if err != nil {
			t.Fatalf("round %d: open: %v", round, err)
		}
		log := New(store, testkit.NewFrozenClock(testInstant), nil)
		for i := 0; i < childAppends; i++ {
			if _, err := log.Append(ctx, sampleEvent(round*childAppends+i)); err != nil {
				t.Fatalf("round %d append %d: %v", round, i, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatalf("round %d: close: %v", round, err)
		}
	}
}

func TestAuditTwoProcessesAppendOverOneStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const children = 2
	cmds := make([]*exec.Cmd, children)
	outs := make([]*childOutput, children)
	for i := range cmds {
		outs[i] = &childOutput{}
		cmds[i] = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuditAppendChild$", "-test.count=1")
		cmds[i].Env = append(os.Environ(), auditChildEnv+"="+path)
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatalf("starting child %d: %v", i, err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, outs[i].buf)
		}
	}
	if t.Failed() {
		return
	}

	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening the shared store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := New(store, testkit.NewFrozenClock(testInstant), nil)
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify after two processes appended: %v", err)
	}
	requireRecordCount(t, store, children*childRounds*childAppends)
}

// childOutput collects a child's combined output for the failure message.
type childOutput struct{ buf []byte }

func (c *childOutput) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// TestTwoLogsSameStoreInterleaveWithoutConflict is the P1-SEC-06 bug in its
// plainest form: two Log values over one store, each appending in turn. On
// the cached-head writer the second Log's second append landed on a
// sequence the first had taken (ErrAlreadyRecorded).
func TestTwoLogsSameStoreInterleaveWithoutConflict(t *testing.T) {
	ctx := context.Background()
	a, store, _, clock := newTestLog(t)
	b := New(store, clock, nil)

	const each = 50
	for i := 0; i < each; i++ {
		if _, err := a.Append(ctx, sampleEvent(i)); err != nil {
			t.Fatalf("log A append %d: %v", i, err)
		}
		if _, err := b.Append(ctx, sampleEvent(each+i)); err != nil {
			t.Fatalf("log B append %d: %v", i, err)
		}
	}
	if err := a.Verify(ctx); err != nil {
		t.Fatalf("Verify after interleaved appends: %v", err)
	}
	requireRecordCount(t, store, 2*each)
}

// requireRecordCount fails unless the store holds exactly want records.
func requireRecordCount(t *testing.T, store provider.Store, want int) {
	t.Helper()
	if got := len(scanNamespace(t, store, recordPrefix)); got != want {
		t.Fatalf("store holds %d records, want %d", got, want)
	}
}

// hookStore runs before once, at the start of the next Tx, then delegates:
// the seam that lets a test move the log between a writer's head read and
// its commit.
type hookStore struct {
	provider.Store
	before func()
}

func (h *hookStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if f := h.before; f != nil {
		h.before = nil
		f()
	}
	return h.Store.Tx(ctx, fn)
}

// TestAppendRetriesWhenHeadMovesBeforeCommit moves the head after the
// writer has read it: the commit loses the head compare-and-swap, the writer
// re-reads, and the record lands after the one that beat it. Nothing forks.
func TestAppendRetriesWhenHeadMovesBeforeCommit(t *testing.T) {
	ctx := context.Background()
	_, store, _, clock := newTestLog(t)
	rival := New(store, clock, nil)
	hooked := &hookStore{Store: store}
	writer := New(hooked, clock, nil)
	if _, err := writer.Append(ctx, sampleEvent(0)); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	hooked.before = func() {
		if _, err := rival.Append(ctx, sampleEvent(1)); err != nil {
			t.Errorf("rival append: %v", err)
		}
	}

	rec, err := writer.Append(ctx, sampleEvent(2))
	if err != nil {
		t.Fatalf("append that lost the race: %v", err)
	}
	if rec.Seq != 3 {
		t.Fatalf("raced append landed at sequence %d, want 3", rec.Seq)
	}
	if err := writer.Verify(ctx); err != nil {
		t.Fatalf("Verify after the race: %v", err)
	}
	requireRecordCount(t, store, 3)
}

// conflictHeadStore makes every head compare-and-swap lose, counting the
// attempts, so no append can ever commit.
type conflictHeadStore struct {
	provider.Store
	txs int
}

func (c *conflictHeadStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	c.txs++
	return c.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, conflictHeadTx{tx})
	})
}

type conflictHeadTx struct{ provider.Tx }

func (c conflictHeadTx) CompareAndSwap(ctx context.Context, ns, key string, old, v []byte) error {
	if key == headKey {
		return cascade.New(cascade.KindConflict, "head moved")
	}
	return c.Tx.CompareAndSwap(ctx, ns, key, old, v)
}

func TestAppendThirdConflictIsKindConflict(t *testing.T) {
	ctx := context.Background()
	_, store, _, clock := newTestLog(t)
	losing := &conflictHeadStore{Store: store}
	log := New(losing, clock, nil)

	_, err := log.Append(ctx, sampleEvent(1))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("append against a store that always conflicts returned %v, want KindConflict", err)
	}
	requireSentinel(t, err, ErrAlreadyRecorded)
	if losing.txs != appendAttempts || appendAttempts != 4 {
		t.Fatalf("store saw %d commits (appendAttempts %d), want the first try plus 3 retries", losing.txs, appendAttempts)
	}
	if n := len(scanNamespace(t, store, "")); n != 0 {
		t.Fatalf("the refused append left %d keys in the store", n)
	}
}

// shapeRedactor tags anything shaped like a GitHub token. It stands in for
// the detector half of the substitution pass; internal/secrets imports this
// package, so the real pass is exercised from cmd/cascade (the same test
// name there).
type shapeRedactor struct{ pattern *regexp.Regexp }

func (r shapeRedactor) Redact(_ context.Context, content []byte) ([]byte, error) {
	return r.pattern.ReplaceAll(content, []byte("<github_token>GHP_1</github_token>")), nil
}

// TestAuditAppendRedactsDetectedShape: a token-shaped string in `action` or
// `explain` that no vault holds is stored as its tag, read back from the
// persisted bytes.
func TestAuditAppendRedactsDetectedShape(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)
	token := "ghp_" + strings.Repeat("aB3dE5", 6)
	log := NewWithRedactor(store, testkit.NewFrozenClock(testInstant), nil,
		shapeRedactor{pattern: regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)})

	event := sampleEvent(1)
	event.Action = "push with " + token
	event.Explain = json.RawMessage(`{"auth":"` + token + `"}`)
	if _, err := log.Append(ctx, event); err != nil {
		t.Fatalf("Append: %v", err)
	}
	stored := string(storedRecord(t, log, 1))
	if strings.Contains(stored, token) || strings.Count(stored, "GHP_1") != 2 {
		t.Fatalf("persisted record keeps the token or misses a tag: %s", stored)
	}
}
