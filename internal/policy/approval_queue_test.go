// Purpose: the approval queue's admission contract over a REAL B-layer
// store — providers/sqlite on a t.TempDir() file (Art.2, Art.7.1), never an
// in-memory double — covering batching, deduplication, expiry, the
// three-field GetPending payload, and the two local-only refusals.
//
// SPORT: internal/policy StoreApprovals/ADDED, PendingEntry/ADDED
// (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// assertStore keeps the provider.Store interface referenced from this file,
// so a future change to the fixture cannot quietly drop the real-store
// requirement without the compiler noticing.
var _ provider.Store = (*sqlite.Driver)(nil)

// --- fixture --------------------------------------------------------------

// approvalCap is the capability every approval test queues against.
func approvalCap() Capability {
	return Capability{
		Name:          "workspace.write",
		Desc:          "write files in the workspace",
		DefaultPolicy: ClassWorkspaceMutation,
	}
}

// seqMinter is a deterministic TokenMinter for tests: it counts. Real
// unguessability is the shipped randomTokenMinter's job; a test that
// depended on random ids could not assert on one.
type seqMinter struct {
	n   int
	err error
}

func (m *seqMinter) Mint(_ context.Context, req ApprovalMintRequest) (ApprovalToken, error) {
	if m.err != nil {
		return ApprovalToken{}, m.err
	}
	m.n++
	return ApprovalToken{
		RequestID:  fmt.Sprintf("req-%04d", m.n),
		Nonce:      fmt.Sprintf("nonce-%04d", m.n),
		ActionHash: req.ActionHash,
		ParamsHash: req.ParamsHash,
		Issued:     req.Issued,
		Expires:    req.Expires,
	}, nil
}

// recordingSink captures the audit rows the queue writes.
type recordingSink struct {
	events []audit.Event
	err    error
}

func (s *recordingSink) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	s.events = append(s.events, e)
	return audit.Record{}, s.err
}

// kinds returns the captured kinds in order.
func (s *recordingSink) kinds() []audit.Kind {
	out := make([]audit.Kind, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Kind)
	}
	return out
}

// listDenyList denies exactly the actions it was built with.
type listDenyList struct {
	denied map[string]bool
	err    error
}

func (d listDenyList) Denied(_ context.Context, action string) (bool, error) {
	if d.err != nil {
		return false, d.err
	}
	return d.denied[action], nil
}

// approvalFixture bundles a queue over a real database file.
type approvalFixture struct {
	path   string
	db     *sqlite.Driver
	reg    *MemoryRegistry
	grants *StoreGrants
	clock  *testkit.FrozenClock
	sink   *recordingSink
	minter *seqMinter
	cfg    ApprovalQueueConfig
	queue  *StoreApprovals
}

// newApprovalFixture opens a real SQLite database in a temp dir and builds
// a queue with a three-deep batch cap and the default ten-second window.
func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	return newApprovalFixtureWith(t, ApprovalQueueConfig{
		Batching: ApprovalBatching{WindowSeconds: 10, Cap: 3},
	})
}

// newApprovalFixtureWith builds a queue, filling in every collaborator the
// caller did not override.
func newApprovalFixtureWith(t *testing.T, cfg ApprovalQueueConfig) *approvalFixture {
	t.Helper()
	f := &approvalFixture{path: filepath.Join(t.TempDir(), "cascade.db")}
	f.reg = NewMemoryRegistry()
	f.clock = testkit.NewFrozenClock(baseTime)
	f.sink = &recordingSink{}
	f.minter = &seqMinter{}
	if err := f.reg.Add(context.Background(), approvalCap()); err != nil {
		t.Fatalf("registering the capability: %v", err)
	}
	f.cfg = cfg
	f.open(t)
	return f
}

// open (re)opens the database and rebuilds the queue over it.
func (f *approvalFixture) open(t *testing.T) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	f.db = db
	grants, err := NewStoreGrants(db, f.reg, f.clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	f.grants = grants
	cfg := f.cfg
	cfg.Store, cfg.Registry, cfg.Grants, cfg.Clock = db, f.reg, grants, f.clock
	if cfg.Recorder == nil {
		cfg.Recorder = f.sink
	}
	if cfg.Minter == nil {
		cfg.Minter = f.minter
	}
	q, err := NewApprovalQueue(cfg)
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}
	f.queue = q
	t.Cleanup(func() { _ = db.Close() })
}

// enqueue admits one action, failing the test on refusal.
func (f *approvalFixture) enqueue(t *testing.T, action string) EnqueueResult {
	t.Helper()
	res, err := f.queue.Enqueue(context.Background(), askRequest(action))
	if err != nil {
		t.Fatalf("Enqueue(%q): %v", action, err)
	}
	return res
}

// askRequest is the canonical L2 request the tests queue.
func askRequest(action string) EnqueueRequest {
	return EnqueueRequest{
		Subject:    testSubject(),
		Capability: approvalCap().Name,
		Level:      L2,
		Action:     action,
		Params:     []byte(`{"path":"a.txt"}`),
		Summary:    "write " + action,
	}
}

// --- construction ---------------------------------------------------------

// TestNewApprovalQueueRequiresCollaborators proves a queue cannot be built
// without a collaborator it would otherwise have to assume something about.
func TestNewApprovalQueueRequiresCollaborators(t *testing.T) {
	f := newApprovalFixture(t)
	full := ApprovalQueueConfig{
		Store: f.db, Registry: f.reg, Grants: f.grants, Clock: f.clock,
	}
	cases := map[string]func(c *ApprovalQueueConfig){
		"store":    func(c *ApprovalQueueConfig) { c.Store = nil },
		"registry": func(c *ApprovalQueueConfig) { c.Registry = nil },
		"grants":   func(c *ApprovalQueueConfig) { c.Grants = nil },
		"clock":    func(c *ApprovalQueueConfig) { c.Clock = nil },
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := full
		cases[name](&cfg)
		if _, err := NewApprovalQueue(cfg); err == nil {
			t.Errorf("NewApprovalQueue with no %s = nil error, want a refusal", name)
		}
	}
	if _, err := NewApprovalQueue(ApprovalQueueConfig{
		Store: f.db, Registry: f.reg, Grants: f.grants, Clock: f.clock,
		Batching: ApprovalBatching{WindowSeconds: -1},
	}); err == nil {
		t.Error("NewApprovalQueue with a negative window = nil error, want a refusal")
	}
	if _, err := NewApprovalQueue(ApprovalQueueConfig{
		Store: f.db, Registry: f.reg, Grants: f.grants, Clock: f.clock,
		Batching: ApprovalBatching{Cap: maxApprovalBatchCap + 1},
	}); err == nil {
		t.Error("NewApprovalQueue with an over-range cap = nil error, want a refusal")
	}
}

// TestNewApprovalQueueDefaultsBatching proves an unset Batching takes the
// 08 §3 numerics rather than zero, which would batch nothing.
func TestNewApprovalQueueDefaultsBatching(t *testing.T) {
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{})
	if got := f.queue.cfg.Batching.WindowSeconds; got != DefaultApprovalBatchWindowSeconds {
		t.Errorf("window = %d, want the 08 §3 default %d", got, DefaultApprovalBatchWindowSeconds)
	}
	if got := f.queue.cfg.Batching.Cap; got != DefaultApprovalBatchCap {
		t.Errorf("cap = %d, want the 08 §3 default %d", got, DefaultApprovalBatchCap)
	}
	if f.queue.cfg.Minter == nil {
		t.Error("a queue built without a minter has none; want the crypto/rand default")
	}
}

// TestNilQueueRefuses proves every method on a nil queue denies rather
// than panicking, so a caller that forgot to wire one cannot proceed.
func TestNilQueueRefuses(t *testing.T) {
	ctx := context.Background()
	var q *StoreApprovals
	if _, err := q.Enqueue(ctx, askRequest("x")); err == nil {
		t.Error("Enqueue on a nil queue = nil error")
	}
	if _, err := q.GetPending(ctx); err == nil {
		t.Error("GetPending on a nil queue = nil error")
	}
	if _, err := q.Decide(ctx, []DecisionRequest{{RequestID: "r"}}); err == nil {
		t.Error("Decide on a nil queue = nil error")
	}
	if err := q.Cancel(ctx, "r"); err == nil {
		t.Error("Cancel on a nil queue = nil error")
	}
	if _, err := q.ConsumeToken(ctx, ConsumeRequest{RequestID: "r"}); err == nil {
		t.Error("ConsumeToken on a nil queue = nil error")
	}
	if _, err := q.Expire(ctx); err == nil {
		t.Error("Expire on a nil queue = nil error")
	}
	var l *Ledger
	if err := l.Consume(ctx, LedgerRecord{Nonce: "n", RequestID: "r"}); err == nil {
		t.Error("Consume on a nil ledger = nil error")
	}
}
