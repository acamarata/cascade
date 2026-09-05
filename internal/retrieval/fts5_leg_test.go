package retrieval_test

// Purpose: the leg's tests — it labels its list with the frozen leg name,
//   it reports "did not run" rather than "found nothing" when no index is
//   configured, it refuses a malformed query instead of skipping, and it
//   re-binds every hit through the scope filter on the way back.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — real driver, TempDir, no clock, no network.
// SPORT: internal.retrieval.Leg/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The leg must satisfy the recall surface's Leg contract, which is the
// production shape it exists to fill.
var _ recall.Leg = (*retrieval.Leg)(nil)

// TestFTS5LegRanksUnderTheFrozenName drives the leg exactly as the recall
// surface drives it, and asserts the list it labels its output with.
func TestFTS5LegRanksUnderTheFrozenName(t *testing.T) {
	h := newIndexedHarness(t)
	list, ran, err := retrieval.NewLeg(h.index).Query(
		context.Background(), h.filter(t, ftsSession), ftsQuery, 10)
	if err != nil {
		t.Fatalf("Leg.Query: %v", err)
	}
	if !ran {
		t.Fatal("a configured leg reported that it did not run")
	}
	if list.Strategy != rrf.StrategyFTS {
		t.Errorf("leg labelled its list %q, want %q", list.Strategy, rrf.StrategyFTS)
	}
	if list.Weight != rrf.NeutralWeight {
		t.Errorf("leg weight = %v, want the neutral weight", list.Weight)
	}
	if len(list.Hits) != 2 {
		t.Fatalf("leg returned %d hits, want the two in-scope matches", len(list.Hits))
	}
	if list.Hits[0].CorpusID != ftsHandbook.ID || list.Hits[0].Trust != ftsHandbook.Trust {
		t.Errorf("hit %+v does not carry the classification the scope filter resolved", list.Hits[0])
	}
	for _, hit := range list.Hits {
		if hit.CorpusID == ftsJournal.ID {
			t.Errorf("out-of-scope corpus reached the leg's list: %+v", hit)
		}
	}
}

// TestFTS5LegFusesWithTheVectorLeg proves the leg's output is usable by
// the ranking core under the name the core reserved for it, which is the
// whole reason the leg exists.
func TestFTS5LegFusesWithTheVectorLeg(t *testing.T) {
	h := newIndexedHarness(t)
	list, _, err := retrieval.NewLeg(h.index).Query(
		context.Background(), h.filter(t, ftsSession), ftsQuery, 10)
	if err != nil {
		t.Fatalf("Leg.Query: %v", err)
	}
	fused, err := rrf.Fuse([]rrf.RankedList{list}, rrf.DefaultK)
	if err != nil {
		t.Fatalf("rrf.Fuse: %v", err)
	}
	if len(fused) != len(list.Hits) {
		t.Fatalf("fusion produced %d results from %d hits", len(fused), len(list.Hits))
	}
	if len(fused[0].Strategies) != 1 || fused[0].Strategies[0] != rrf.StrategyFTS {
		t.Errorf("fused result names %v as its contributing legs", fused[0].Strategies)
	}
}

// TestFTS5LegDegradesRatherThanLying: an unconfigured leg reports that it
// did not run. An empty list would be indistinguishable from an index
// that holds nothing.
func TestFTS5LegDegradesRatherThanLying(t *testing.T) {
	h := newIndexedHarness(t)
	list, ran, err := retrieval.NewLeg(nil).Query(
		context.Background(), h.filter(t, ftsSession), ftsQuery, 10)
	if err != nil {
		t.Fatalf("an unconfigured leg must not be an error, got %v", err)
	}
	if ran {
		t.Error("an unconfigured leg reported that it ran")
	}
	if len(list.Hits) != 0 {
		t.Errorf("an unconfigured leg returned %d hits", len(list.Hits))
	}
}

// TestFTS5LegRefusesRatherThanSkipping: a malformed query is an error, not
// a leg that quietly found nothing. Returning an empty list would tell the
// caller their query ran and matched nothing, which is not what happened.
func TestFTS5LegRefusesRatherThanSkipping(t *testing.T) {
	h := newIndexedHarness(t)
	leg := retrieval.NewLeg(h.index)
	filter := h.filter(t, ftsSession)
	for _, raw := range []string{"", "   ", "-", `foo "bar`, "-fusion"} {
		_, ran, err := leg.Query(context.Background(), filter, raw, 10)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Query(%q) returned %v, want KindInvalidInput", raw, err)
		}
		if ran {
			t.Errorf("Query(%q) reported that the leg ran despite refusing", raw)
		}
	}
}

// TestFTS5LegGuardsItsArguments covers the leg's own input refusals and
// the empty-scope case, which reports a leg that RAN and matched nothing.
func TestFTS5LegGuardsItsArguments(t *testing.T) {
	h := newIndexedHarness(t)
	leg := retrieval.NewLeg(h.index)
	if _, _, err := leg.Query(context.Background(), nil, ftsQuery, 10); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("a nil scope filter returned %v, want KindInvalidInput", err)
	}
	if _, _, err := leg.Query(context.Background(), h.filter(t, ftsSession), ftsQuery, 0); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("topK 0 returned %v, want KindInvalidInput", err)
	}
	stranger := newFTSHarness(t)
	stranger.addCorpus(t, ftsHandbook)
	list, ran, err := leg.Query(context.Background(),
		stranger.filter(t, corpusQueryFor(ftsJournal)), ftsQuery, 10)
	if err != nil || !ran {
		t.Fatalf("an empty scope must run and match nothing, got ran=%v err=%v", ran, err)
	}
	if len(list.Hits) != 0 {
		t.Errorf("a session authorized to read nothing received %d hits", len(list.Hits))
	}
}
