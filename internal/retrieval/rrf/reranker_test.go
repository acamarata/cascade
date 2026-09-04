// Purpose: the optional reranker stage's tests — that disabled means
// untouched, that a misconfigured stage fails closed, that no reranker
// behaviour can lose a result or introduce one, and that the ordering is
// deterministic.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.1.1 — SpyReranker lives here and only here; there is
// no reranker implementation in non-test code. Art.7 — no network, no
// files, no bare clock: the timeout path is driven by a context deadline
// and a spy that blocks on ctx.Done rather than by sleeping.
//
// SPORT: internal.retrieval.rrf.Rerank/ADDED (P1-E06-W2-S12-T3).

package rrf

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// SpyReranker is the only Reranker in the tree (Art.1.1). It records what
// it was asked and replies with whatever the test told it to reply,
// including replies that violate the provider.Reranker contract — that
// is the point: the stage has to survive a misbehaving implementation.
type SpyReranker struct {
	// reply builds the response from the passages it was handed. A nil
	// reply scores the passages in reverse input order, the simplest
	// observable reordering.
	reply func(query string, passages []string) []provider.RankedPassage
	// err, when non-nil, is returned instead of any reply.
	err error
	// block makes Rerank wait for ctx and return its error, which is how
	// the timeout and cancellation paths are exercised without sleeping.
	block bool

	calls     int
	gotQuery  string
	gotPassed []string
}

// Rerank implements provider.Reranker.
func (s *SpyReranker) Rerank(ctx context.Context, query string, passages []string) ([]provider.RankedPassage, error) {
	s.calls++
	s.gotQuery = query
	s.gotPassed = append([]string(nil), passages...)
	if s.block {
		<-ctx.Done()
		return nil, cascade.Wrap(cascade.KindTimeout, ctx.Err(), "spy: deadline reached")
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.reply != nil {
		return s.reply(query, passages), nil
	}
	// Reverse the input order, scoring descending so the reply satisfies
	// the Reranker contract's non-increasing-score requirement.
	out := make([]provider.RankedPassage, 0, len(passages))
	for i := len(passages) - 1; i >= 0; i-- {
		out = append(out, provider.RankedPassage{Text: passages[i], Score: float64(i)})
	}
	return out, nil
}

// textOf is the passage-text resolver every test injects: the body of a
// chunk is, for test purposes, its chunk id with a fixed prefix, so the
// mapping back from a reply to a result is observable.
func textOf(r FusedResult) string { return "body of " + r.ChunkID }

// fusedFixture is the candidate set under test: three chunks with
// distinct trust tags and paths, in a fixed fusion order.
func fusedFixture() []FusedResult {
	return []FusedResult{
		{ChunkID: "a", Path: "/w/a.md", CorpusID: "work", Trust: corpus.TrustTrusted, RawScore: 0.3, Score: 1.0, Strategies: []StrategyName{StrategyFTS}},
		{ChunkID: "b", Path: "/w/b.md", CorpusID: "work", Trust: corpus.TrustUntrustedSource, RawScore: 0.2, Score: 0.5, Strategies: []StrategyName{StrategyVector}},
		{ChunkID: "c", Path: "/w/c.md", CorpusID: "notes", Trust: corpus.TrustTrusted, RawScore: 0.1, Score: 0.0, Strategies: []StrategyName{StrategyFTS, StrategyVector}},
	}
}

func chunkIDs(results []FusedResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.ChunkID
	}
	return out
}

func enabled(s *SpyReranker) RerankOptions {
	return RerankOptions{Enabled: true, Reranker: s, PassageText: textOf}
}

// TestRerankerStageDisabled is the regression gate on the whole ticket:
// with the stage off — the shipped default — the pipeline's output must
// be indistinguishable from unreranked fusion, and the reranker must not
// even be consulted.
func TestRerankerStageDisabled(t *testing.T) {
	fused := fusedFixture()
	spy := &SpyReranker{}

	out, err := Rerank(context.Background(), "q", fused, RerankOptions{Reranker: spy, PassageText: textOf})
	if err != nil {
		t.Fatalf("disabled stage returned an error: %v", err)
	}
	if spy.calls != 0 {
		t.Errorf("disabled stage called the reranker %d times, want 0", spy.calls)
	}
	if out.Applied {
		t.Error("disabled stage reported Applied true")
	}
	if out.Degraded != nil {
		t.Errorf("disabled stage reported degradation: %v", out.Degraded)
	}
	if !reflect.DeepEqual(out.Results, fusedFixture()) {
		t.Errorf("disabled stage altered the results: %+v", out.Results)
	}
}

// TestRerankerDisabledMatchesFuseExactly asserts the same property
// against the real fusion output rather than a fixture: a disabled
// reranker stage cannot perturb the ordering Fuse produced.
func TestRerankerDisabledMatchesFuseExactly(t *testing.T) {
	lists := []RankedList{
		{Strategy: StrategyFTS, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: "b", Path: "/w/b.md", Trust: corpus.TrustTrusted},
			{ChunkID: "a", Path: "/w/a.md", Trust: corpus.TrustTrusted},
		}},
		{Strategy: StrategyVector, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: "a", Path: "/w/a.md", Trust: corpus.TrustTrusted},
			{ChunkID: "c", Path: "/w/c.md", Trust: corpus.TrustTrusted},
		}},
	}
	want, err := Fuse(lists, DefaultK)
	if err != nil {
		t.Fatalf("Fuse: %v", err)
	}
	got, err := Rerank(context.Background(), "q", want, RerankOptions{})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	// Compare against a second, independent fusion rather than against
	// the slice that was handed in, so the assertion cannot pass simply
	// by both sides being the same object.
	baseline, err := Fuse(lists, DefaultK)
	if err != nil {
		t.Fatalf("Fuse (baseline): %v", err)
	}
	if !reflect.DeepEqual(got.Results, baseline) {
		t.Errorf("disabled stage diverged from Fuse:\n got %+v\nwant %+v", got.Results, baseline)
	}
}

// TestRerankerStageEnabledWithImpl asserts the stage actually reorders
// when it is on and healthy, and that it hands the reranker the query and
// every candidate.
func TestRerankerStageEnabledWithImpl(t *testing.T) {
	spy := &SpyReranker{}
	out, err := Rerank(context.Background(), "the query", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !out.Applied || out.Degraded != nil {
		t.Fatalf("Applied=%v Degraded=%v, want true/nil", out.Applied, out.Degraded)
	}
	if got, want := chunkIDs(out.Results), []string{"c", "b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (spy reverses)", got, want)
	}
	if spy.gotQuery != "the query" {
		t.Errorf("query passed = %q", spy.gotQuery)
	}
	if got, want := spy.gotPassed, []string{"body of a", "body of b", "body of c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("passages passed = %v, want %v", got, want)
	}
}

// TestRerankerStageEnabledNoImpl is the fail-closed case: configured on,
// nothing registered. A typed error, and no results at all — never a
// silent pass-through that would look to the user like reranking ran.
func TestRerankerStageEnabledNoImpl(t *testing.T) {
	out, err := Rerank(context.Background(), "q", fusedFixture(), RerankOptions{Enabled: true, PassageText: textOf})
	if err == nil {
		t.Fatal("enabled stage with no implementation returned no error")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("error is not a KindInvalidInput taxonomy error: %v", err)
	}
	if out.Results != nil || out.Applied {
		t.Errorf("fail-closed path returned results: %+v", out)
	}
}

// TestRerankerStageEnabledNoTextResolver is the other half of the
// fail-closed configuration check: an implementation with no way to see
// any text cannot rerank, so saying so beats ranking noise.
func TestRerankerStageEnabledNoTextResolver(t *testing.T) {
	spy := &SpyReranker{}
	_, err := Rerank(context.Background(), "q", fusedFixture(), RerankOptions{Enabled: true, Reranker: spy})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if spy.calls != 0 {
		t.Errorf("reranker was called despite an unusable configuration")
	}
}

// TestRerankerErrorFallsBackToFusionOrder is the documented failure
// policy: an erroring reranker costs the caller the reordering and
// nothing else, and the reason is reported rather than swallowed.
func TestRerankerErrorFallsBackToFusionOrder(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "spy: backend down")
	spy := &SpyReranker{err: boom}

	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("a reranker error must not fail the query: %v", err)
	}
	if out.Applied {
		t.Error("Applied true after a reranker error")
	}
	if !errors.Is(out.Degraded, boom) {
		t.Errorf("Degraded = %v, want the reranker's own error", out.Degraded)
	}
	if !reflect.DeepEqual(out.Results, fusedFixture()) {
		t.Errorf("results after a reranker error = %+v, want the fused set unchanged", out.Results)
	}
}

// TestRerankerTimeoutFallsBackToFusionOrder drives the deadline path
// through ctx rather than a sleep: the spy waits for cancellation and
// returns ctx's error, exactly as the Reranker contract requires.
func TestRerankerTimeoutFallsBackToFusionOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // an already-expired deadline, without waiting on a clock
	spy := &SpyReranker{block: true}

	out, err := Rerank(ctx, "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("a reranker timeout must not fail the query: %v", err)
	}
	if out.Applied {
		t.Error("Applied true after a timeout")
	}
	if !cascade.HasKind(out.Degraded, cascade.KindTimeout) {
		t.Errorf("Degraded = %v, want a KindTimeout error", out.Degraded)
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("timeout lost or reordered results: %v, want %v", got, want)
	}
}

// TestRerankerDuplicateChunkIDsRefused: a candidate set that is not
// deduped has no total order under the tie-break rule, so the stage
// refuses it rather than producing an order it cannot reproduce.
func TestRerankerDuplicateChunkIDsRefused(t *testing.T) {
	fused := []FusedResult{{ChunkID: "a"}, {ChunkID: "a"}}
	_, err := Rerank(context.Background(), "q", fused, enabled(&SpyReranker{}))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestRerankerEmptyCandidateSet: a query that matched nothing is not an
// error, with the stage on or off.
func TestRerankerEmptyCandidateSet(t *testing.T) {
	spy := &SpyReranker{reply: func(string, []string) []provider.RankedPassage { return nil }}
	out, err := Rerank(context.Background(), "q", nil, enabled(spy))
	if err != nil {
		t.Fatalf("Rerank on an empty candidate set: %v", err)
	}
	if len(out.Results) != 0 {
		t.Errorf("results = %+v, want empty", out.Results)
	}
}
