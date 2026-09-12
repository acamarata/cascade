package sync

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
)

func newTestEgressEngine(t *testing.T) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	registry := egress.NewRegistry()
	if err := registry.Register(egress.EgressClassSync, egress.InterceptConfig{Enabled: true, Owner: "test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	eng, err := egress.NewEngine(registry, testVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

type testVault struct{}

func (testVault) List(context.Context) ([]string, error)      { return nil, nil }
func (testVault) Get(context.Context, string) ([]byte, error) { return nil, nil }

func TestEngineSendBatchFiltersAndAdvancesCursor(t *testing.T) {
	e := NewEngine(newFakeStore(), fakeClock{t: time.Now()}, newTestEgressEngine(t))
	recs := []Record{
		{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-1", Tier: egress.TierInternal, Payload: []byte("a")},
		{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-2", Tier: egress.TierRestricted, Payload: []byte("b")},
	}
	var wire bytes.Buffer
	cur, err := e.SendBatch(context.Background(), &wire, storage.DomainMemory, "memory", recs, 1, 0)
	if err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if cur.Position != 2 {
		t.Fatalf("cursor position = %d, want 2 (both records accounted for: one applied, one excluded)", cur.Position)
	}
	excl, err := e.Cursors().Exclusion(context.Background(), storage.DomainMemory, "memory", "rec-2")
	if err != nil {
		t.Fatalf("Exclusion: %v", err)
	}
	if excl.Reason != "sensitivity-restricted" {
		t.Fatalf("exclusion reason = %q, want sensitivity-restricted", excl.Reason)
	}
	if wire.Len() == 0 {
		t.Fatal("SendBatch must have written the admitted record onto the wire")
	}
}

func TestEngineSendBatchRequiresEgress(t *testing.T) {
	e := NewEngine(newFakeStore(), fakeClock{t: time.Now()}, nil)
	recs := []Record{{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-1", Tier: egress.TierInternal, Payload: []byte("a")}}
	var wire bytes.Buffer
	if _, err := e.SendBatch(context.Background(), &wire, storage.DomainMemory, "memory", recs, 1, 0); err == nil {
		t.Fatal("SendBatch without an egress engine must fail closed")
	}
}

func TestEngineSendBatchWriteFailurePropagates(t *testing.T) {
	e := NewEngine(newFakeStore(), fakeClock{t: time.Now()}, newTestEgressEngine(t))
	recs := []Record{{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-1", Tier: egress.TierInternal, Payload: []byte("a")}}
	if _, err := e.SendBatch(context.Background(), erroringWriter{}, storage.DomainMemory, "memory", recs, 1, 0); err == nil {
		t.Fatal("SendBatch must propagate a wire write failure")
	}
}

func TestEngineNextPositionRecoversFromCorruptCursor(t *testing.T) {
	store := newFakeStore()
	_ = store.Put(context.Background(), cursorNamespace, cursorKey(storage.DomainMemory, "memory"), []byte("not json"))
	e := NewEngine(store, fakeClock{t: time.Now()}, newTestEgressEngine(t))
	recs := []Record{{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-1", Tier: egress.TierInternal, Payload: []byte("a")}}
	var wire bytes.Buffer
	// nextPosition treats an unreadable cursor as position zero rather
	// than propagating the corruption mid-batch; the subsequent Advance
	// call still surfaces it (a corrupt cursor cannot be silently healed).
	_, err := e.SendBatch(context.Background(), &wire, storage.DomainMemory, "memory", recs, 1, 0)
	if err == nil {
		t.Fatal("a corrupt persisted cursor must still surface once Advance re-reads it")
	}
}

func TestEngineSendBatchAllExcludedAdvancesCursorWithoutEgress(t *testing.T) {
	e := NewEngine(newFakeStore(), fakeClock{t: time.Now()}, newTestEgressEngine(t))
	recs := []Record{{Domain: storage.DomainConfig, Subkind: "accounts", ID: "rec-1", Tier: egress.TierRestricted}}
	var wire bytes.Buffer
	cur, err := e.SendBatch(context.Background(), &wire, storage.DomainConfig, "accounts", recs, 1, 0)
	if err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if cur.Position != 1 {
		t.Fatalf("cursor position = %d, want 1", cur.Position)
	}
}
