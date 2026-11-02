package embed

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// interruptedLedger fails only ledger writes, after the vector upsert.
type interruptedLedger struct {
	provider.Store
	err    error
	writes int
}

func (s *interruptedLedger) Put(ctx context.Context, ns, key string, value []byte) error {
	if ns == ledgerNamespace {
		s.writes++
		if s.err != nil {
			return s.err
		}
	}
	return s.Store.Put(ctx, ns, key, value)
}

func TestEmbedCrashBetweenUpsertAndLedgerConverges(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	h := newHarness(t, 2)
	ctx := context.Background()
	interruption := cascade.New(cascade.KindUnavailable, "ledger interrupted after vector upsert")
	writer := &interruptedLedger{Store: h.store, err: interruption}
	h.pipeline.ledger.store = writer
	req := Request{Corpus: testCorpus("crash"), Chunks: chunks(5)}
	first, err := h.pipeline.Run(ctx, req)
	if err == nil || errors.Unwrap(err) != interruption || interruption.Error() != "unavailable: ledger interrupted after vector upsert" {
		t.Fatalf("first run: result=%+v err=%v cause=%v", first, err, errors.Unwrap(err))
	}
	if writer.writes != 1 || h.count(t, req.Corpus.ID) != 2 {
		t.Fatalf("injection missed crash boundary: writes=%d vectors=%d", writer.writes, h.count(t, req.Corpus.ID))
	}
	for _, chunk := range req.Chunks {
		seen, readErr := h.pipeline.ledger.seen(ctx, first.Namespace, chunk.ID, testModel)
		if readErr != nil || seen {
			t.Fatalf("ledger advanced before crash: seen=%t err=%v", seen, readErr)
		}
	}
	restarted, err := New(h.embedder, h.vectors, h.store, runtime.NewFixedClock(fixedInstant), 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restarted.Run(ctx, req)
	if err != nil || second.Upserted != len(req.Chunks) || second.Skipped != 0 {
		t.Fatalf("restart: result=%+v err=%v", second, err)
	}
	assertConverged(t, h, req)
	third, err := restarted.Run(ctx, req)
	if err != nil || third.Skipped != len(req.Chunks) || third.Embedded != 0 {
		t.Fatalf("completed ledger did not suppress retry: result=%+v err=%v", third, err)
	}
}

func assertConverged(t *testing.T, h *harness, req Request) {
	t.Helper()
	ctx := context.Background()
	ns := fusion.NamespaceFor(req.Corpus.ID)
	if got := h.count(t, req.Corpus.ID); got != len(req.Chunks) {
		t.Fatalf("vectors=%d, want %d", got, len(req.Chunks))
	}
	matches, err := h.vectors.Query(ctx, ns, provider.VectorQuery{Values: vectorFor("query", testModel.Dimensions), TopK: 20})
	if err != nil || len(matches) != len(req.Chunks) {
		t.Fatalf("vector enumeration: matches=%v err=%v", matches, err)
	}
	ids := make(map[string]int)
	for _, match := range matches {
		ids[match.ID]++
	}
	for _, chunk := range req.Chunks {
		if ids[chunk.ID] != 1 {
			t.Fatalf("chunk %s has %d vectors", chunk.ID, ids[chunk.ID])
		}
		data, err := h.store.Get(ctx, ledgerNamespace, ledgerKey(ns, chunk.ID))
		var entry ledgerEntry
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.ModelID != testModel.ID || entry.Dimensions != testModel.Dimensions || !entry.EmbeddedAt.Equal(fixedInstant) {
			t.Fatalf("incomplete ledger entry: %+v", entry)
		}
	}
}
