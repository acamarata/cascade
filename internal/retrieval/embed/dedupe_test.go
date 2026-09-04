// Purpose: the dedupe ledger's own behaviour, at the level below the
// pipeline: what it keys on, what it refuses, and what it does with a
// store that misbehaves.

package embed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestLedgerRecordsThenSees(t *testing.T) {
	store := storetest.NewMemStore()
	l := ledger{store: store}
	ns := fusion.NamespaceFor("notes")
	c := newChunk("a.md", "a body")
	ctx := context.Background()

	seen, err := l.seen(ctx, ns, c.ID, testModel)
	if err != nil || seen {
		t.Fatalf("seen before recording = %v, %v; want false, nil", seen, err)
	}
	if err := l.record(ctx, ns, []retrieval.Chunk{c}, testModel, fixedInstant); err != nil {
		t.Fatalf("record: %v", err)
	}
	seen, err = l.seen(ctx, ns, c.ID, testModel)
	if err != nil || !seen {
		t.Fatalf("seen after recording = %v, %v; want true, nil", seen, err)
	}
}

func TestLedgerIsPerNamespace(t *testing.T) {
	store := storetest.NewMemStore()
	l := ledger{store: store}
	c := newChunk("a.md", "a body")
	ctx := context.Background()
	if err := l.record(ctx, fusion.NamespaceFor("personal"), []retrieval.Chunk{c},
		testModel, fixedInstant); err != nil {
		t.Fatalf("record: %v", err)
	}
	seen, err := l.seen(ctx, fusion.NamespaceFor("project"), c.ID, testModel)
	if err != nil {
		t.Fatalf("seen: %v", err)
	}
	if seen {
		t.Error("content recorded for one corpus counted as embedded in another")
	}
}

func TestLedgerKeysOnContentNotPath(t *testing.T) {
	store := storetest.NewMemStore()
	l := ledger{store: store}
	ns := fusion.NamespaceFor("notes")
	ctx := context.Background()
	if err := l.record(ctx, ns, []retrieval.Chunk{newChunk("a.md", "shared body")},
		testModel, fixedInstant); err != nil {
		t.Fatalf("record: %v", err)
	}
	sameContentElsewhere := newChunk("b.md", "shared body")
	seen, err := l.seen(ctx, ns, sameContentElsewhere.ID, testModel)
	if err != nil {
		t.Fatalf("seen: %v", err)
	}
	if !seen {
		t.Error("identical content at another path was not recognized as embedded")
	}
	edited := newChunk("a.md", "shared body, edited")
	seen, err = l.seen(ctx, ns, edited.ID, testModel)
	if err != nil {
		t.Fatalf("seen: %v", err)
	}
	if seen {
		t.Error("edited content was treated as already embedded")
	}
}

func TestLedgerRefusesADifferentModelForTheSameContent(t *testing.T) {
	store := storetest.NewMemStore()
	l := ledger{store: store}
	ns := fusion.NamespaceFor("notes")
	c := newChunk("a.md", "a body")
	ctx := context.Background()
	if err := l.record(ctx, ns, []retrieval.Chunk{c}, testModel, fixedInstant); err != nil {
		t.Fatalf("record: %v", err)
	}
	other := provider.EmbedModel{ID: "other-embed-v2", Dimensions: 4}
	seen, err := l.seen(ctx, ns, c.ID, other)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict rather than a dedupe miss", err)
	}
	if seen {
		t.Error("a refused lookup also reported the content as seen")
	}
}

func TestLedgerRecordsTheInjectedInstant(t *testing.T) {
	store := storetest.NewMemStore()
	l := ledger{store: store}
	ns := fusion.NamespaceFor("notes")
	c := newChunk("a.md", "a body")
	ctx := context.Background()
	if err := l.record(ctx, ns, []retrieval.Chunk{c}, testModel, fixedInstant); err != nil {
		t.Fatalf("record: %v", err)
	}
	data, err := store.Get(ctx, ledgerNamespace, ledgerKey(ns, c.ID))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var entry ledgerEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("decoding the entry: %v", err)
	}
	if !entry.EmbeddedAt.Equal(fixedInstant) {
		t.Errorf("embedded_at = %v, want the injected instant %v", entry.EmbeddedAt, fixedInstant)
	}
	if entry.ModelID != testModel.ID || entry.Dimensions != testModel.Dimensions {
		t.Errorf("entry model = %s/%d, want %s/%d",
			entry.ModelID, entry.Dimensions, testModel.ID, testModel.Dimensions)
	}
}

func TestLedgerReportsACorruptEntry(t *testing.T) {
	store := &brokenStore{Store: storetest.NewMemStore(), corruptIn: ledgerNamespace}
	l := ledger{store: store}
	_, err := l.seen(context.Background(), fusion.NamespaceFor("notes"), "abc", testModel)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity for an undecodable entry", err)
	}
}

func TestLedgerPropagatesStoreFailures(t *testing.T) {
	ctx := context.Background()
	ns := fusion.NamespaceFor("notes")
	c := newChunk("a.md", "a body")

	readFails := ledger{store: &brokenStore{Store: storetest.NewMemStore(), failGetIn: ledgerNamespace}}
	if _, err := readFails.seen(ctx, ns, c.ID, testModel); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("seen err = %v, want KindUnavailable", err)
	}
	writeFails := ledger{store: &brokenStore{Store: storetest.NewMemStore(), failPutIn: ledgerNamespace}}
	err := writeFails.record(ctx, ns, []retrieval.Chunk{c}, testModel, fixedInstant)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("record err = %v, want KindUnavailable", err)
	}
}

func TestRunPropagatesALedgerReadFailure(t *testing.T) {
	// The ledger read happens after the namespace binding is claimed, so
	// the broken store must let the binding through and fail only the
	// ledger namespace.
	p := newBrokenHarness(t, &brokenStore{
		Store: storetest.NewMemStore(), failGetIn: ledgerNamespace,
	})
	_, err := p.Run(context.Background(), Request{Corpus: testCorpus("notes"), Chunks: chunks(1)})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the ledger read", err)
	}
}

func TestRunPropagatesALedgerWriteFailure(t *testing.T) {
	p := newBrokenHarness(t, &brokenStore{
		Store: storetest.NewMemStore(), failPutIn: ledgerNamespace,
	})
	_, err := p.Run(context.Background(), Request{Corpus: testCorpus("notes"), Chunks: chunks(1)})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the ledger write", err)
	}
}
